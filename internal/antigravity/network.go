package antigravity

// NetworkError carries a safe, actionable proxy configuration error. Raw HTTP
// errors may contain authorization codes or credentials and stay private.
type NetworkError struct{ Message string }

func (e *NetworkError) Error() string { return e.Message }
