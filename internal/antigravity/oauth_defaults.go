package antigravity

// Public installed-application OAuth configuration. This is a protocol value,
// not a private AppForce/user secret. Provenance and the MIT notice are recorded
// in docs/third-party-notices.md. Account adapters are implemented in this package.
func defaultOAuthConfig() OAuthConfig {
	return OAuthConfig{ClientID: "1071006060591-tmhssin2h21lcre235vtolojh4g403ep.apps.googleusercontent.com", ClientSecret: "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", Cloud: "daily"}
}
