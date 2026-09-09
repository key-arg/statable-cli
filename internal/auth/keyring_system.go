package auth

import (
	"errors"

	"github.com/zalando/go-keyring"
)

// SystemKeyring is the real backend: Keychain on macOS, Secret Service on
// Linux, Credential Manager on Windows.
type SystemKeyring struct{}

func (SystemKeyring) Get(service, account string) (string, error) {
	v, err := keyring.Get(service, account)
	if err != nil {
		if errors.Is(err, keyring.ErrNotFound) {
			return "", err
		}
		return "", ErrKeyringUnavailable
	}
	return v, nil
}

func (SystemKeyring) Set(service, account, secret string) error {
	if err := keyring.Set(service, account, secret); err != nil {
		return ErrKeyringUnavailable
	}
	return nil
}

func (SystemKeyring) Delete(service, account string) error {
	return keyring.Delete(service, account)
}
