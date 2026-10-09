package codex

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"time"

	"agent-platform/backend/internal/review"
)

type workerResponse struct {
	Profile         *Profile        `json:"profile,omitempty"`
	Report          json.RawMessage `json:"report,omitempty"`
	ErrorCode       string          `json:"errorCode,omitempty"`
	SandboxVerified bool            `json:"sandboxVerified,omitempty"`
}

func decodeWorkerResponse(data []byte) (workerResponse, error) {
	var message workerResponse
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&message); err != nil {
		return workerResponse{}, err
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return workerResponse{}, review.ErrInvalidFindings
	}
	return message, nil
}

// ServeWorker is the image entry point. It accepts only platform-selected review
// inputs; deployment options arrive separately from the fixed Docker command.
func ServeWorker(ctx context.Context, config ExecConfig, probe bool, input io.Reader, output io.Writer) error {
	var request review.RunInput
	if !probe {
		const maxInput = 6*review.MaxReviewPatchBytes + 16384
		content, readErr := io.ReadAll(io.LimitReader(input, maxInput+1))
		decoder := json.NewDecoder(bytes.NewReader(content))
		decoder.DisallowUnknownFields()
		if readErr != nil || len(content) > maxInput || decoder.Decode(&request) != nil || decoder.Decode(new(any)) != io.EOF || request.Validate() != nil {
			return json.NewEncoder(output).Encode(workerResponse{ErrorCode: "review_input_invalid"})
		}
	}
	runner, err := NewExecRunner(ctx, config)
	message := workerResponse{}
	if err != nil {
		message.ErrorCode = workerErrorCode(err)
	} else if probe {
		if verifyLinuxSandbox(ctx, runner.Profile()) != nil {
			message.ErrorCode = "runtime_sandbox_unavailable"
		} else {
			profile := runner.Profile()
			message.Profile = &profile
			message.SandboxVerified = true
		}
	} else {
		report, runErr := runner.Run(ctx, request)
		if runErr != nil {
			message.ErrorCode = workerErrorCode(runErr)
		} else {
			message.Report, err = json.Marshal(report)
			if err != nil {
				message.ErrorCode = "review_findings_invalid"
			}
		}
	}
	return json.NewEncoder(output).Encode(message)
}

func verifyLinuxSandbox(ctx context.Context, profile Profile) error {
	home, err := os.MkdirTemp("", "agent-platform-sandbox-check-")
	if err != nil {
		return ErrIncompatibleRuntime
	}
	defer func() { _ = os.RemoveAll(home) }()
	inspection, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	command := exec.CommandContext(inspection, profile.BinaryPath, "--no-daemon", "--ask-for-approval", "never", "sandbox", "-c", `sandbox_mode="read-only"`, "/bin/true")
	command.Env = []string{"PATH=/usr/bin:/bin", "HOME=" + home, "CODEX_HOME=" + home}
	command.Dir = home
	command.Stdout = io.Discard
	command.Stderr = io.Discard
	command.WaitDelay = time.Second
	return command.Run()
}

func workerErrorCode(err error) string {
	for _, code := range []string{"review_canceled", "review_timeout", "review_input_invalid", "review_findings_invalid", "review_output_unavailable", "review_output_too_large"} {
		if errors.Is(err, workerError(code)) {
			return code
		}
	}
	return "review_execution_failed"
}
func workerError(code string) error {
	switch code {
	case "review_canceled":
		return context.Canceled
	case "review_timeout":
		return context.DeadlineExceeded
	case "review_input_invalid":
		return review.ErrInvalidRunInput
	case "review_findings_invalid":
		return review.ErrInvalidFindings
	case "review_output_unavailable":
		return review.ErrOutputUnavailable
	case "review_output_too_large":
		return review.ErrOutputTooLarge
	default:
		return review.ErrExecutionFailed
	}
}
