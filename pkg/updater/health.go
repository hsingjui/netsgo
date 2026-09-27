package updater

import (
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"netsgo/internal/svcmgr"
	"netsgo/pkg/protocol"
)

const (
	upgradeHealthTimeout       = 30 * time.Second
	upgradeHealthPollInterval  = 500 * time.Millisecond
	upgradeHealthStableWindow  = 8 * time.Second
	serverHealthRequestTimeout = time.Second
)

type serviceHealthCheckDeps struct {
	IsActive    func(unit string) (bool, error)
	ProbeServer func() error
	Now         func() time.Time
	Sleep       func(time.Duration)
}

var verifyStartedServicesFunc = defaultVerifyStartedServices

func defaultVerifyStartedServices(units []string) error {
	return verifyStartedServices(units, serviceHealthCheckDeps{
		IsActive:    svcmgr.IsActive,
		ProbeServer: probeServerManagementAPI,
		Now:         time.Now,
		Sleep:       time.Sleep,
	}, upgradeHealthTimeout, upgradeHealthStableWindow, upgradeHealthPollInterval)
}

func verifyStartedServices(units []string, deps serviceHealthCheckDeps, timeout, stableWindow, pollInterval time.Duration) error {
	if len(units) == 0 {
		return nil
	}

	serverUnit := svcmgr.UnitName(svcmgr.RoleServer)
	hasServer := false
	for _, unit := range units {
		if unit == serverUnit {
			hasServer = true
			break
		}
	}

	startedAt := deps.Now()
	deadline := startedAt.Add(timeout)
	var stableSince time.Time
	var lastProblems []string

	for {
		now := deps.Now()
		healthy := true
		problems := make([]string, 0, len(units)+1)
		for _, unit := range units {
			active, err := deps.IsActive(unit)
			switch {
			case err != nil:
				healthy = false
				problems = append(problems, fmt.Sprintf("%s 状态检查失败: %v", unit, err))
			case !active:
				healthy = false
				problems = append(problems, fmt.Sprintf("%s 未处于 active 状态", unit))
			}
		}
		if healthy && hasServer {
			if err := deps.ProbeServer(); err != nil {
				healthy = false
				problems = append(problems, fmt.Sprintf("%s 管理端口尚未就绪: %v", serverUnit, err))
			}
		}

		if healthy {
			if stableSince.IsZero() {
				stableSince = now
			}
			if now.Sub(stableSince) >= stableWindow {
				return nil
			}
		} else {
			stableSince = time.Time{}
		}
		lastProblems = problems

		if !now.Before(deadline) {
			if len(lastProblems) == 0 {
				lastProblems = append(lastProblems, fmt.Sprintf("服务未能连续稳定运行 %s", stableWindow))
			}
			return fmt.Errorf("在 %s 内未进入稳定运行状态: %s", timeout, strings.Join(lastProblems, "；"))
		}

		sleepFor := pollInterval
		if remaining := deadline.Sub(now); sleepFor > remaining {
			sleepFor = remaining
		}
		deps.Sleep(sleepFor)
	}
}

func probeServerManagementAPI() error {
	layout := newServiceLayoutFunc(svcmgr.RoleServer)
	env, err := svcmgr.ReadServerEnv(layout)
	if err != nil {
		return fmt.Errorf("read server env: %w", err)
	}
	port := env.Port
	if port == 0 {
		port = 9527
	}

	scheme := "http"
	transport := &http.Transport{
		Proxy:             nil,
		DialContext:       (&net.Dialer{Timeout: serverHealthRequestTimeout}).DialContext,
		DisableKeepAlives: true,
	}
	if env.TLSMode == "auto" || env.TLSMode == "custom" {
		scheme = "https"
		// This probe never leaves loopback. Certificate trust is irrelevant here;
		// receiving any HTTP response proves that the upgraded process is serving.
		transport.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12, InsecureSkipVerify: true} //nolint:gosec
	}
	client := &http.Client{Transport: transport, Timeout: serverHealthRequestTimeout}
	defer transport.CloseIdleConnections()

	address := net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
	// The internal data-channel route is resolved before HTTP tunnel Host
	// dispatch. A non-upgrade request returns 426 without contacting a target.
	req, err := http.NewRequest(http.MethodGet, scheme+"://"+address+"/ws/data", nil)
	if err != nil {
		return err
	}
	req.Header.Set("Sec-WebSocket-Protocol", protocol.WSSubProtocolData)
	req.Host = net.JoinHostPort("localhost", strconv.Itoa(port))
	req.Close = true
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	return nil
}

func startupHealthFailure(units []string, backupPath string, databaseRestored bool, healthErr, rollbackErr error) error {
	journalCommand := journalCommandForUnits(units)
	guidance := fmt.Sprintf(
		"旧版本备份保留在 %s；请运行 %q 查看日志；若日志提示 migration 012 存在孤儿安全记录，请先备份 /var/lib/netsgo/server/netsgo.db，检查报错表中不在 admin_users 的 user_id，恢复缺失用户，或只处理已确认归属和处置方式的记录，然后重新升级",
		backupPath,
		journalCommand,
	)
	if rollbackErr != nil {
		return fmt.Errorf("新版本服务启动后未通过健康检查，已尝试自动回滚但未完整完成: %w；回滚错误: %v；%s", healthErr, rollbackErr, guidance)
	}
	restored := "旧二进制和服务环境"
	if databaseRestored {
		restored = "旧二进制、服务环境和服务端数据库"
	}
	return fmt.Errorf("新版本服务启动后未通过健康检查，已自动恢复%s并重启原服务: %w；%s", restored, healthErr, guidance)
}

func journalCommandForUnits(units []string) string {
	args := make([]string, 0, len(units)*2)
	for _, unit := range units {
		args = append(args, "-u", unit)
	}
	return "sudo journalctl " + strings.Join(args, " ") + " -n 200 --no-pager"
}
