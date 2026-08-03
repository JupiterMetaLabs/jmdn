package DB_OPs

// CountBuilder provides backward-compatible count helpers.
// ThebeDB does not expose prefix-scan counts via the store interface;
// callers that display these as stats degrade to 0 gracefully.
type CountBuilder struct{}

func (cb CountBuilder) Build() (*CountBuilder, error)            { return &CountBuilder{}, nil }
func (cb CountBuilder) GetMainDBCount(_ string) (int, error)     { return 0, nil }
func (cb CountBuilder) GetAccountsDBCount(_ string) (int, error) { return 0, nil }
