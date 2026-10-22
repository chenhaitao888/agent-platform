package review

import "errors"

// Output diagnostics contain only a fixed code. Never attach rejected model
// content, file names, decoder errors or process output to this error.
type outputError struct {
	code  string
	cause error
}

func (e *outputError) Error() string { return e.cause.Error() + ": " + e.code }
func (e *outputError) Unwrap() error { return e.cause }

// NewOutputError reconstructs a safe error across the Worker boundary. Unknown
// codes are discarded; callers decoding wire data must validate them first.
func NewOutputError(code string) error {
	cause := outputDiagnosticCause(code)
	if cause == nil {
		return ErrInvalidFindings
	}
	return &outputError{code: code, cause: cause}
}

func ValidOutputDiagnostic(code string) bool { return outputDiagnosticCause(code) != nil }

// OutputDiagnostic returns only platform-owned codes, including conservative
// fallbacks for older Workers which do not send a detailed diagnostic.
func OutputDiagnostic(err error) string {
	var diagnostic *outputError
	if errors.As(err, &diagnostic) {
		return diagnostic.code
	}
	for _, code := range []string{"findings_invalid", "output_unavailable", "output_too_large"} {
		if errors.Is(err, outputDiagnosticCause(code)) {
			return code
		}
	}
	return ""
}

func outputDiagnosticCause(code string) error {
	switch code {
	case "output_unavailable":
		return ErrOutputUnavailable
	case "output_too_large":
		return ErrOutputTooLarge
	case "findings_invalid", "findings_too_large", "encoding_invalid", "expected_refs_invalid",
		"json_invalid", "shape_invalid", "types_invalid", "version_invalid", "refs_mismatch",
		"too_many_findings", "text_invalid", "severity_invalid", "confidence_invalid",
		"location_invalid", "credential_in_output", "worker_protocol_invalid":
		return ErrInvalidFindings
	default:
		return nil
	}
}
