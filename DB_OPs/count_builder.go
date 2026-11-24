package DB_OPs

// CountBuilder provides backward-compatible count helpers.
// ThebeDB does not expose prefix-scan counts via the store interface;
// callers that display these as stats degrade to 0 gracefully.
type CountBuilder struct{}
