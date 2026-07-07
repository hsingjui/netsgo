package manage

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	clientstate "netsgo/internal/client"
	"netsgo/internal/svcmgr"
)

func showClientDetails(deps clientDeps) error {
	inspection := deps.Inspect()
	layout := svcmgr.NewLayout(svcmgr.RoleClient)
	env, envErr := loadClientEnv(deps)
	localStateSummary, localStateErr := clientLocalStateSummary(layout)

	rows := [][2]string{
		{"服务", layout.ServiceName},
		{"角色", string(svcmgr.RoleClient)},
		{"状态", lifecycleStateLabel(inspection.State)},
		{"已安装", boolLabel(inspection.State == svcmgr.StateInstalled)},
		{"运行中", boolStateLabel(inspection.State == svcmgr.StateInstalled, deps.IsActive)},
		{"已启用", boolStateLabel(inspection.State == svcmgr.StateInstalled, deps.IsEnabled)},
		{"二进制路径", layout.BinaryPath},
		{"数据目录", layout.DataDir},
		{"数据路径", clientDataPath(layout)},
		{"锁路径", lockPath(layout.DataDir)},
		{"日志目标", "journald"},
		{"Unit 路径", layout.UnitPath},
		{"Env 路径", layout.EnvPath},
		{"运行用户", layout.RunAsUser},
		{"服务地址", stringOrUnavailable(env.Server, envErr)},
		{"跳过 TLS 校验", boolOrUnavailable(env.TLSSkipVerify, envErr)},
		{"TLS 指纹", stringOrUnavailable(env.TLSFingerprint, envErr)},
		{"Client 本地状态", stringOrUnavailable(localStateSummary, localStateErr)},
	}
	if envErr != nil {
		rows = append(rows, [2]string{"Env 状态", fmt.Sprintf("不可用（%v）", envErr)})
	}
	rows = appendProblemRows(rows, inspection.Problems)
	deps.UI.PrintSummary("Client 检查", rows)
	return nil
}

func loadClientEnv(deps clientDeps) (svcmgr.ClientEnv, error) {
	if deps.ReadClientEnv == nil {
		return svcmgr.ClientEnv{}, nil
	}
	return deps.ReadClientEnv()
}

func clientDataPath(layout svcmgr.ServiceLayout) string {
	return layout.RuntimeDir
}

func clientLocalStateSummary(layout svcmgr.ServiceLayout) (string, error) {
	path := filepath.Join(clientDataPath(layout), clientstate.ClientDBFileName)
	state, ok, err := clientstate.LoadClientIdentity(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "本地状态文件未发现", nil
		}
		return "", err
	}
	if !ok {
		return "本地状态文件存在，但内容不可用", nil
	}

	if state.InstallID == "" && state.Token == "" && state.TLSFingerprint == "" {
		return "本地状态文件存在，但内容不可用", nil
	}
	return "已保存本地连接状态", nil
}

func boolOrUnavailable(value bool, err error) string {
	if err != nil {
		return fmt.Sprintf("不可用（%v）", err)
	}
	return boolLabel(value)
}
