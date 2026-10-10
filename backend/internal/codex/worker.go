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
	Profile          *Profile        `json:"profile,omitempty"`
	Report           json.RawMessage `json:"report,omitempty"`
	ErrorCode        string          `json:"errorCode,omitempty"`
	SandboxVerified  bool            `json:"sandboxVerified,omitempty"`
	GatewaySupported bool            `json:"gatewaySupported,omitempty"`
}

// This envelope is used only by deployment-selected gateway Workers. HTTP
// requests still carry only RunInput coordinates, never these credentials.
type workerGatewayRequest struct {
	Input      review.RunInput `json:"input"`
	Credential json.RawMessage `json:"credential"`
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
		var credentialContent json.RawMessage
		var decodeErr error
		if config.Gateway == nil {
			decodeErr = decoder.Decode(&request)
		} else {
			var envelope workerGatewayRequest
			decodeErr = decoder.Decode(&envelope)
			request, credentialContent = envelope.Input, envelope.Credential
		}
		if readErr != nil || len(content) > maxInput || decodeErr != nil || decoder.Decode(new(any)) != io.EOF || request.Validate() != nil {
			return json.NewEncoder(output).Encode(workerResponse{ErrorCode: "review_input_invalid"})
		}
		if config.Gateway != nil {
			gateway, err := validatedGateway(config.Gateway)
			if err != nil {
				return json.NewEncoder(output).Encode(workerResponse{ErrorCode: "review_input_invalid"})
			}
			credential, err := parseGatewayCredential(credentialContent, gateway.ServicePrincipal, config.Timeout)
			if err != nil {
				return json.NewEncoder(output).Encode(workerResponse{ErrorCode: "review_credentials_unavailable"})
			}
			// The credential never becomes a Worker file or CLI argument.
			gateway.CredentialFile = ""
			config.Gateway = gateway
			config.gatewayCredential = &credential
		}
	}
	runner, err := NewExecRunner(ctx, config)
	message := workerResponse{}
	if err != nil {
		message.ErrorCode = workerErrorCode(err)
	} else if probe {
		if verifyLinuxSandbox(ctx, runner) != nil {
			message.ErrorCode = "runtime_sandbox_unavailable"
		} else {
			profile := runner.Profile()
			message.Profile = &profile
			message.SandboxVerified = true
			message.GatewaySupported = true
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

func verifyLinuxSandbox(ctx context.Context, runner *ExecRunner) error {
	home, err := os.MkdirTemp("", "agent-platform-sandbox-check-")
	if err != nil {
		return ErrIncompatibleRuntime
	}
	defer func() { _ = os.RemoveAll(home) }()
	inspection, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	args := []string{"--no-daemon", "--ask-for-approval", "never"}
	args = append(args, runner.gateway.arguments()...)
	args = append(args, "sandbox", "-c", `sandbox_mode="read-only"`, "/bin/true")
	command := exec.CommandContext(inspection, runner.Profile().BinaryPath, args...)
	command.Env = []string{"PATH=/usr/bin:/bin", "HOME=" + home, "CODEX_HOME=" + home}
	command.Dir = home
	command.Stdout = io.Discard
	command.Stderr = io.Discard
	command.WaitDelay = time.Second
	return command.Run()
}

func workerErrorCode(err error) string {
	for _, code := range []string{"review_canceled", "review_timeout", "review_input_invalid", "review_findings_invalid", "review_output_unavailable", "review_output_too_large", "review_credentials_unavailable"} {
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
	case "review_credentials_unavailable":
		return ErrGatewayCredentialUnavailable
	default:
		return review.ErrExecutionFailed
	}
}
