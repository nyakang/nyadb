package connectioneditor

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/egoist/mygo/ui"

	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/ui/theme"
	"github.com/nyakang/nyadb/locales"
)

type mysqlTestResult struct {
	generation uint64
	err        error
}

type mysqlConnectionTester func(context.Context, connections.ActiveSettings, string) error

// The UI thread is the only owner of State. Background work sends immutable
// results to a buffered channel and asks MyGo to schedule the next frame.
func (s *State) startMySQLConnectionTest(services ui.Services) {
	if !s.active || s.connectionTesting {
		return
	}
	s.connectionTestMessage = ""
	s.connectionError = ""
	// The Test button checks the configuration currently shown, not the
	// separate saved Active selection. Neither draft nor secret is persisted.
	p, err := s.draftProfile()
	if err != nil {
		s.connectionError = err.Error()
		return
	}
	p.Variants = nil
	p.ActiveVariant = ""
	var settings connections.ActiveSettings
	if p.Kind.IsMySQLFamily() {
		settings, err = connections.ResolveActiveSettings(p)
	} else {
		p, err = connections.NormalizeProfile(p)
	}
	if err == nil && p.Kind.IsMySQLFamily() {
		if s.validateMySQL == nil {
			err = fmt.Errorf("%s", locales.Text("MySQL 连接测试服务不可用"))
		} else {
			err = s.validateMySQL(settings, s.connectionSecret)
		}
	}
	if err != nil {
		s.connectionError = err.Error()
		return
	}
	if p.Kind == connections.Neo4j {
		cfg, e := connections.Neo4jProfileConfig(p)
		if e != nil || connections.ValidateNeo4jCredential(cfg, s.connectionSecret) != nil {
			s.validationError(fieldError("密码", "常规", "请输入 Neo4j 密码"))
			return
		}
	}
	s.connectionTestGeneration++
	generation := s.connectionTestGeneration
	ctx, cancel := context.WithCancel(context.Background())
	s.connectionTestCancel = cancel
	sentinelPassword := s.redisSentinelPassword
	if p.Kind == connections.Redis {
		ctx = connections.WithRedisSentinelPassword(ctx, sentinelPassword)
	}
	s.connectionTesting = true
	secret := s.connectionSecret // never log or persist this value
	tunnelSecrets := s.tunnelSnapshot()
	tunnelTester := s.mysqlTunnelTester
	tester := s.mysqlConnectionTester
	providerTest := s.connectionTester
	if tester == nil {
		tester = func(context.Context, connections.ActiveSettings, string) error {
			return fmt.Errorf("%s", locales.Text("MySQL 连接测试服务不可用"))
		}
	}
	results := make(chan mysqlTestResult, 1)
	s.connectionTestResults = results
	go func() {
		var err error
		if (settings.MySQLAdvanced.SSH.Enabled || settings.MySQLAdvanced.HTTP.Enabled) && tunnelTester != nil {
			err = tunnelTester(ctx, settings, secret, tunnelSecrets)
		} else if providerTest != nil {
			err = providerTest(ctx, p, secret)
		} else if !p.Kind.IsMySQLFamily() {
			err = fmt.Errorf("%s", locales.Text("连接测试服务不可用"))
		} else {
			err = tester(ctx, settings, secret)
		}
		if err != nil {
			for _, sensitive := range []string{secret, sentinelPassword, tunnelSecrets.SSHPassword, tunnelSecrets.SSHPassphrase, tunnelSecrets.HTTPToken, tunnelSecrets.ProxyPassword, tunnelSecrets.HTTPKeyPassphrase} {
				if sensitive != "" {
					err = errors.New(strings.ReplaceAll(err.Error(), sensitive, "[已隐藏]"))
				}
			}
		}
		if err != nil && secret != "" {
			// Never show a returned driver error with the exact password.
			err = errors.New(strings.ReplaceAll(err.Error(), secret, "[已隐藏]"))
		}
		results <- mysqlTestResult{generation: generation, err: err}
		services.Invalidate()
	}()
}

func (s *State) consumeMySQLConnectionTest() {
	if !s.connectionTesting || s.connectionTestResults == nil {
		return
	}
	select {
	case result := <-s.connectionTestResults:
		if result.generation != s.connectionTestGeneration {
			return
		}
		s.connectionTesting = false
		s.connectionTestCancel = nil
		s.connectionTestResults = nil
		if result.err == nil {
			s.connectionTestMessage = fmt.Sprintf(locales.Text("%s 连接测试成功（已关闭测试会话）"), connectionKindName(s.connectionDraft.Kind))
			s.connectionError = ""
		} else if errors.Is(result.err, context.Canceled) {
			s.connectionTestMessage = locales.Text("连接测试已取消")
			s.connectionError = ""
		} else {
			s.connectionTestMessage = ""
			s.connectionError = fmt.Sprintf(locales.Text("连接测试失败：%v"), result.err)
		}
	default:
	}
}

func (s *State) cancelMySQLConnectionTest() {
	s.connectionTestGeneration++
	if s.connectionTestCancel != nil {
		s.connectionTestCancel()
	}
	s.connectionTestCancel = nil
	s.connectionTestResults = nil
	s.connectionTesting = false
	s.connectionTestMessage = ""
}

func (s *State) connectionTestStatus(c *ui.Context) {
	if s.connectionTesting {
		ui.Text(c, fmt.Sprintf(locales.Text("正在测试 %s 连接…"), connectionKindName(s.connectionDraft.Kind))).FontSize(theme.Font(c, 12))
	} else if s.connectionTestMessage != "" {
		ui.Text(c, s.connectionTestMessage).FontSize(theme.Font(c, 12))
	}
}
