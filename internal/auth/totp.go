package auth

import (
	"github.com/pquerna/otp"
	"github.com/pquerna/otp/totp"
)

func GenerateTOTPKey(username string) (*otp.Key, error) {
	return totp.Generate(totp.GenerateOpts{
		Issuer:      "Janus",
		AccountName: username,
	})
}

func ValidateTOTPCode(secret, code string) bool {
	return totp.Validate(code, secret)
}
