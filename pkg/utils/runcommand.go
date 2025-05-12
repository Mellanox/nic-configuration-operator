package utils

import (
	"bytes"
	"fmt"
	"strings"

	execUtils "k8s.io/utils/exec"
)

// RunCommand runs a command and captures stderr separately for better error reporting
// Returns stdout, error (with stderr included in the error message if command fails)
func RunCommand(cmd execUtils.Cmd) ([]byte, error) {
	var stderr bytes.Buffer
	cmd.SetStderr(&stderr)
	stdout, err := cmd.Output()
	if err != nil {
		stderrOutput := strings.TrimSpace(stderr.String())
		if stderrOutput != "" {
			err = fmt.Errorf("%w: %s", err, stderrOutput)
		}
	}

	return stdout, err
}
