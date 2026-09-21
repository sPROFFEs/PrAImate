package studio

import (
	"context"
	"errors"
	"io"
	"strings"
	"time"

	"git.jtsec.local/lab/PrAImate/internal/installer"
)

type installMethod struct {
	ID             string   `json:"id"`
	Label          string   `json:"label"`
	Command        string   `json:"command"`
	Recommended    bool     `json:"recommended"`
	MissingPrereqs []string `json:"missingPrereqs,omitempty"`
}

func cliInstallMethods(cli string) ([]installMethod, []installer.Method) {
	var methods []installer.Method
	if cli == "praimate-code" {
		methods = installer.ToolMethods(installer.ToolPraimateCode, installer.ActionInstall, installer.DetectOS())
	} else {
		methods = installer.Methods(installer.AgentID(cli), installer.ActionInstall, installer.DetectOS())
	}
	out := make([]installMethod, 0, len(methods))
	for _, method := range methods {
		out = append(out, installMethod{ID: method.ID, Label: method.Label, Command: method.Command, Recommended: method.Recommended,
			MissingPrereqs: installer.BlockingPrereqs(method, installer.RunOptions{InstallNode: true})})
	}
	return out, methods
}

type installWriter struct{ server *Server }

func (w installWriter) Write(body []byte) (int, error) {
	for _, line := range strings.Split(strings.TrimRight(string(body), "\n"), "\n") {
		if line != "" {
			w.server.Broadcast("install.output", map[string]string{"line": line})
		}
	}
	return len(body), nil
}

func (s *Server) installCLI(ctx context.Context, cli, methodID string) error {
	_, methods := cliInstallMethods(cli)
	for _, method := range methods {
		if method.ID != methodID {
			continue
		}
		ctx, cancel := context.WithTimeout(ctx, 15*time.Minute)
		defer cancel()
		writer := io.Writer(installWriter{server: s})
		err := installer.RunWithOptions(ctx, method, installer.RunOptions{InstallNode: true}, writer, writer)
		installer.ImportPnpmPathIfPresent()
		installer.ImportManagedAgentsToPath()
		installer.ImportManagedToolsToPath()
		installer.ImportPraimateBinToPath()
		installer.ImportUserBinDirs()
		installer.ImportWindowsRegistryPath()
		return err
	}
	return errors.New("unknown or unavailable CLI install method")
}
