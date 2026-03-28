package tools

import "context"

type credentialsKey struct{}

// Credential is a short-lived credential token available to tools during execution.
type Credential struct {
	Provider  string
	Token     string
	Scopes    []string
	ExpiresIn int
}

// WithCredentials returns a new context carrying the given credentials.
func WithCredentials(ctx context.Context, creds []Credential) context.Context {
	return context.WithValue(ctx, credentialsKey{}, creds)
}

// CredentialsFromContext retrieves credentials from the context, if present.
func CredentialsFromContext(ctx context.Context) []Credential {
	creds, _ := ctx.Value(credentialsKey{}).([]Credential)
	return creds
}

// CredentialForProvider returns the credential for a specific provider, or nil.
func CredentialForProvider(ctx context.Context, provider string) *Credential {
	for _, c := range CredentialsFromContext(ctx) {
		if c.Provider == provider {
			return &c
		}
	}
	return nil
}
