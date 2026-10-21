package shell

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/services/connections"
)

type mysqlTestResult struct {
	generation uint64
	err        error
}

type mysqlConnectionTester func(context.Context, connections.ActiveSettings, string) error

// The UI thread is the only owner of State. Background work sends immutable
// results to a buffered channel and asks MyGo to schedule the next frame.
func (s *State) startMySQLConnectionTest(services ui.Services) {
	if !s.connectionDialog || s.connectionStep != 1 ||
		s.connectionDraft.Kind != connections.MySQL || s.connectionTesting {
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
	settings, err := connections.ResolveActiveSettings(p)
	if err == nil {
		_, err = connections.DirectMySQLConfig(settings, s.connectionSecret)
	}
	if err != nil {
		s.connectionError = err.Error()
		return
	}
	s.connectionTestGeneration++
	generation := s.connectionTestGeneration
	ctx, cancel := context.WithCancel(context.Background())
	s.connectionTestCancel = cancel
	s.connectionTesting = true
	secret := s.connectionSecret // never log or persist this value
	tester := s.mysqlConnectionTester
	if tester == nil {
		tester = connections.TestDirectMySQLConnection
	}
	results := make(chan mysqlTestResult, 1)
	s.connectionTestResults = results
	go func() {
		err := tester(ctx, settings, secret)
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
			s.connectionTestMessage = "MySQL 连接测试成功（已关闭测试会话）"
			s.connectionError = ""
		} else if errors.Is(result.err, context.Canceled) {
			s.connectionTestMessage = "连接测试已取消"
			s.connectionError = ""
		} else {
			s.connectionTestMessage = ""
			s.connectionError = fmt.Sprintf("连接测试失败：%v", result.err)
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
		ui.Text(c, "正在测试 MySQL 连接…").FontSize(12)
	} else if s.connectionTestMessage != "" {
		ui.Text(c, s.connectionTestMessage).FontSize(12)
	}
}
