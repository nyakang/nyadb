package connectioneditor

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/locales"
)

func (s *State) draftProfile() (connections.Profile, error) {
	p := s.connectionDraft
	p.Name = strings.TrimSpace(p.Name)
	if p.Name == "" {
		return p, fieldError("连接名称", "常规", "请输入连接名称")
	}
	if err := s.advanceEngineOptions(&p); err != nil {
		return p, err
	}
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
		return p, fieldError("连接名称", "常规", "请输入连接名称")
	}
	if p.Kind == connections.SQLite {
		if strings.TrimSpace(p.Database) == "" {
			return p, fieldError("数据库文件", "常规", "请选择 SQLite 数据库文件")
		}
		return p, nil
	}
	if p.Kind == connections.DuckDB {
		if strings.TrimSpace(p.Database) == "" {
			return p, fieldError("数据库文件", "常规", "请输入 DuckDB 文件路径或 :memory:")
		}
		return p, nil
	}
	if p.Kind == connections.Oracle && strings.TrimSpace(p.Database) == "" {
		return p, fieldError("服务名 / SID", "常规", "请输入 Oracle 服务名或 SID")
	}
	if p.Kind == connections.SQLServer && strings.TrimSpace(p.Database) == "" {
		return p, fieldError("初始数据库", "常规", "请输入此连接绑定的 SQL Server 数据库")
	}
	if p.Kind.IsPostgreSQLFamily() && strings.TrimSpace(p.Database) == "" {
		if p.Kind == connections.Kingbase {
			return p, fieldError("初始数据库", "常规", "请输入此连接绑定的 KingbaseES 数据库")
		}
		if p.Kind == connections.GaussDB {
			return p, fieldError("初始数据库", "常规", "请输入此连接绑定的 GaussDB 数据库")
		}
		if p.Kind == connections.OpenGauss {
			return p, fieldError("初始数据库", "常规", "请输入此连接绑定的 openGauss 数据库")
		}
		if p.Kind == connections.CockroachDB {
			return p, fieldError("初始数据库", "常规", "请输入此连接绑定的 CockroachDB 数据库")
		}
		return p, fieldError("初始数据库", "常规", "请输入此连接绑定的 PostgreSQL 数据库")
	}
	port, err := strconv.ParseUint(strings.TrimSpace(s.connectionPort), 10, 16)
	if err != nil || port == 0 {
		return p, fieldError("端口", "常规", "端口必须是 1–65535 之间的数字")
	}
	p.Port = uint16(port)
	if strings.TrimSpace(p.Host) == "" {
		return p, fieldError("主机", "常规", "请输入数据库主机")
	}
	return p, nil
}

func (s *State) Save() {
	var p connections.Profile
	var err error
	if s.connectionDraft.ID != 0 && s.connectionDraft.Kind.IsMySQLFamily() {
		p, err = s.prepareConnectionConfigurations()
	} else {
		p, err = s.draftProfile()
	}
	if err != nil {
		s.validationError(err)
		return
	}
	if s.profileService == nil {
		s.connectionError = locales.Text("连接档案尚未接入本地存储")
		return
	}
	s.prepareInitialSQLRefs(&p)
	var before connections.Profile
	if p.ID > 0 {
		before, err = s.profileService.Get(context.Background(), p.ID)
		if err != nil {
			s.connectionError = locales.Text("读取原连接失败：") + err.Error()
			return
		}
	}
	var saved connections.Profile
	var writtenInitialSQL []string
	completed := false
	defer func() {
		if s.cleanupInitialSQL == nil {
			return
		}
		candidates := writtenInitialSQL
		if completed {
			if before.MySQLAdvanced.InitialSQLID != "" {
				candidates = append(candidates, before.MySQLAdvanced.InitialSQLID)
			}
			for _, v := range before.Variants {
				if v.MySQLAdvanced.InitialSQLID != "" {
					candidates = append(candidates, v.MySQLAdvanced.InitialSQLID)
				}
			}
		}
		if len(candidates) > 0 {
			if err := s.cleanupInitialSQL(candidates); err != nil {
				s.connectionError += "；" + locales.Text("初始 SQL 清理失败；记录已保留，可重试") + ": " + err.Error()
			}
		}
	}()
	if p.ID > 0 {
		saved, err = s.profileService.Update(context.Background(), p)
	} else {
		saved, err = s.profileService.Create(context.Background(), p)
	}
	if err != nil {
		s.connectionError = locales.Text("保存失败（连接名称可能重复或配置无效）：") + err.Error()
		return
	}
	var undoDatabaseSecrets func() error
	if saved.Kind.IsMySQLFamily() {
		writtenInitialSQL, err = s.persistInitialSQL(saved)
		if err != nil {
			var rollbackErr error
			if p.ID == 0 {
				rollbackErr = s.profileService.Delete(context.Background(), saved.ID)
			} else {
				_, rollbackErr = s.profileService.Update(context.Background(), before)
			}
			s.connectionError = locales.Text("加密初始 SQL 保存失败，已尝试恢复原配置：") + errors.Join(err, rollbackErr).Error()
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
			s.connectionError = locales.Text("读取旧数据库凭据失败（已尝试恢复连接配置）：") + err.Error()
			return
		}
		if err := s.persistConnectionPasswords(saved, before); err != nil {
			if p.ID == 0 {
				if rollbackErr := s.profileService.Delete(context.Background(), saved.ID); rollbackErr != nil {
					err = fmt.Errorf(locales.Text("%w；回滚新连接失败：%v"), err, rollbackErr)
				}
			} else if _, rollbackErr := s.profileService.Update(context.Background(), before); rollbackErr != nil {
				err = fmt.Errorf(locales.Text("%w；回滚连接配置失败：%v"), err, rollbackErr)
			}
			s.connectionError = locales.Text("系统密钥库保存密码失败（连接配置已尝试回滚）：") + err.Error()
			return
		}
	}
	if saved.Kind.IsPostgreSQLFamily() || saved.Kind == connections.ClickHouse || saved.Kind == connections.SQLServer || saved.Kind == connections.Oracle || saved.Kind == connections.Dameng || saved.Kind == connections.MongoDB || saved.Kind == connections.Redis {
		s.connectionMainConfig = s.currentConnectionSnapshot()
		if err := s.persistConnectionPasswords(saved, before); err != nil {
			if p.ID == 0 {
				err = errors.Join(err, s.profileService.Delete(context.Background(), saved.ID))
			} else {
				_, restoreErr := s.profileService.Update(context.Background(), before)
				err = errors.Join(err, restoreErr)
			}
			s.connectionError = locales.Text("系统密钥库保存密码失败（连接配置已尝试回滚）：") + err.Error()
			return
		}
	}
	if saved.Kind.IsMySQLFamily() {
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
			s.connectionError = locales.Text("隧道凭据保存失败（已尝试恢复连接配置）：") + err.Error()
			return
		}
	}
	completed = true
	s.Close()
	if s.saved != nil {
		s.saved(saved)
	}
}
