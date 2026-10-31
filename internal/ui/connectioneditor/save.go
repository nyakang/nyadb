package connectioneditor

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/nyakang/nyadb/internal/services/connections"
)

func (s *State) draftProfile() (connections.Profile, error) {
	p := s.connectionDraft
	if err := s.advanceDraft(&p); err != nil {
		return p, err
	}
	if err := s.advanceMySQLSSH(&p); err != nil {
		return p, err
	}
	if err := s.advanceMySQLHTTP(&p); err != nil {
		return p, err
	}
	p.Name = strings.TrimSpace(p.Name)
	if p.Name == "" {
		return p, fmt.Errorf("请输入连接名称")
	}
	if p.Kind == connections.SQLite {
		if strings.TrimSpace(p.Database) == "" {
			return p, fmt.Errorf("请选择 SQLite 数据库文件")
		}
		return p, nil
	}
	if p.Kind == connections.PostgreSQL && strings.TrimSpace(p.Database) == "" {
		return p, fmt.Errorf("请输入此连接绑定的 PostgreSQL 数据库")
	}
	port, err := strconv.ParseUint(strings.TrimSpace(s.connectionPort), 10, 16)
	if err != nil || port == 0 {
		return p, fmt.Errorf("端口必须是 1–65535 之间的数字")
	}
	p.Port = uint16(port)
	if strings.TrimSpace(p.Host) == "" {
		return p, fmt.Errorf("请输入数据库主机")
	}
	return p, nil
}

func (s *State) Save() {
	var p connections.Profile
	var err error
	if s.connectionDraft.ID != 0 && s.connectionDraft.Kind == connections.MySQL {
		p, err = s.prepareConnectionConfigurations()
	} else {
		p, err = s.draftProfile()
	}
	if err != nil {
		s.connectionError = err.Error()
		return
	}
	if s.profileService == nil {
		s.connectionError = "连接档案尚未接入本地存储"
		return
	}
	s.prepareInitialSQLRefs(&p)
	var before connections.Profile
	if p.ID > 0 {
		before, err = s.profileService.Get(context.Background(), p.ID)
		if err != nil {
			s.connectionError = "读取原连接失败：" + err.Error()
			return
		}
	}
	var saved connections.Profile
	if p.ID > 0 {
		saved, err = s.profileService.Update(context.Background(), p)
	} else {
		saved, err = s.profileService.Create(context.Background(), p)
	}
	if err != nil {
		s.connectionError = "保存失败（连接名称可能重复或配置无效）：" + err.Error()
		return
	}
	var undoDatabaseSecrets func() error
	if saved.Kind == connections.MySQL {
		if err := s.persistInitialSQL(saved); err != nil {
			if p.ID == 0 {
				_ = s.profileService.Delete(context.Background(), saved.ID)
			} else {
				_, _ = s.profileService.Update(context.Background(), before)
			}
			s.connectionError = "加密初始 SQL 保存失败，已尝试恢复原配置：" + err.Error()
			return
		}
		undoDatabaseSecrets, err = s.connectionPasswordUndo(saved, before)
		if err != nil {
			if p.ID == 0 {
				err = errors.Join(err, s.profileService.Delete(context.Background(), saved.ID))
			} else {
				_, rollbackErr := s.profileService.Update(context.Background(), before)
				err = errors.Join(err, rollbackErr)
			}
			s.connectionError = "读取旧数据库凭据失败（已尝试恢复连接配置）：" + err.Error()
			return
		}
		if err := s.persistConnectionPasswords(saved, before); err != nil {
			if p.ID == 0 {
				if rollbackErr := s.profileService.Delete(context.Background(), saved.ID); rollbackErr != nil {
					err = fmt.Errorf("%w；回滚新连接失败：%v", err, rollbackErr)
				}
			} else if _, rollbackErr := s.profileService.Update(context.Background(), before); rollbackErr != nil {
				err = fmt.Errorf("%w；回滚连接配置失败：%v", err, rollbackErr)
			}
			s.connectionError = "系统密钥库保存密码失败（连接配置已尝试回滚）：" + err.Error()
			return
		}
	}
	if saved.Kind == connections.PostgreSQL {
		s.connectionMainConfig = s.currentConnectionSnapshot()
		if err := s.persistConnectionPasswords(saved, before); err != nil {
			if p.ID == 0 {
				err = errors.Join(err, s.profileService.Delete(context.Background(), saved.ID))
			} else {
				_, restoreErr := s.profileService.Update(context.Background(), before)
				err = errors.Join(err, restoreErr)
			}
			s.connectionError = "系统密钥库保存密码失败（连接配置已尝试回滚）：" + err.Error()
			return
		}
	}
	if saved.Kind == connections.MySQL {
		if err := s.persistTunnelSecrets(saved, before); err != nil {
			if undoDatabaseSecrets != nil {
				err = errors.Join(err, undoDatabaseSecrets())
			}
			if p.ID == 0 {
				err = errors.Join(err, s.profileService.Delete(context.Background(), saved.ID))
			} else {
				_, rollbackErr := s.profileService.Update(context.Background(), before)
				err = errors.Join(err, rollbackErr)
			}
			s.connectionError = "隧道凭据保存失败（已尝试恢复连接配置）：" + err.Error()
			return
		}
	}
	s.Close()
	if s.saved != nil {
		s.saved(saved)
	}
}
