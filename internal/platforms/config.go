package platforms

import (
	"fmt"
	"os"
)

type OAuthConfig struct{ ClientID, ClientSecret, RedirectURL string }

func LoadOAuth(platform string) (OAuthConfig, error) {
	prefix := platform + "_OAUTH_"
	c := OAuthConfig{ClientID: os.Getenv(prefix + "CLIENT_ID"), ClientSecret: os.Getenv(prefix + "CLIENT_SECRET"), RedirectURL: os.Getenv(prefix + "REDIRECT_URL")}
	if c.ClientID == "" || c.ClientSecret == "" || c.RedirectURL == "" {
		return OAuthConfig{}, fmt.Errorf("%s OAuth credentials are not configured", platform)
	}
	return c, nil
}
