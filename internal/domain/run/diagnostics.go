package run

import "time"

// CheckError preserves the cause of a database coordination failure.
type CheckError struct {
	Stage     string
	MySQLCode uint16
	Err       error
}

func (e *CheckError) Error() string { return e.Err.Error() }
func (e *CheckError) Unwrap() error { return e.Err }

// FailureDetail records the failing probe without persisting raw driver errors.
type FailureDetail struct {
	Source     string    `json:"source"`
	Stage      string    `json:"stage,omitempty"`
	Reason     string    `json:"reason"`
	FailedAt   time.Time `json:"failed_at"`
	DurationMS int64     `json:"duration_ms"`
	TimeoutMS  int64     `json:"timeout_ms"`
	MySQLCode  uint16    `json:"mysql_error_code,omitempty"`
}
