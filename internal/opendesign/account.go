package opendesign

import (
	"strings"

	"github.com/berry-shake/cpa-provider-core/credential"
	"github.com/berry-shake/cpa-provider-core/jsonfield"
	"github.com/berry-shake/cpa-provider-core/provider"
)

// The OpenDesign account signed in on a credential's card (vela device
// authorization) lives in the credential's auth file. The control key reads
// the account's plan windows; it is never sent to the management page.
const (
	KeyAccountControlKey    = "account_control_key"
	KeyAccountControlKeyID  = "account_control_key_id"
	KeyAccountAPIURL        = "account_api_url"
	KeyAccountUserID        = "account_user_id"
	KeyAccountEmail         = "account_email"
	KeyAccountName          = "account_name"
	KeyAccountWorkspaceID   = "account_workspace_id"
	KeyAccountWorkspaceName = "account_workspace_name"
	KeyAccountLoggedInAt    = "account_logged_in_at"
)

// AccountKeys are the auth file keys of a signed-in account; signing out
// removes them all.
var AccountKeys = []string{
	KeyAccountControlKey, KeyAccountControlKeyID, KeyAccountAPIURL, KeyAccountUserID, KeyAccountEmail,
	KeyAccountName, KeyAccountWorkspaceID, KeyAccountWorkspaceName, KeyAccountLoggedInAt,
}

// Account is the OpenDesign account signed in on a credential.
type Account struct {
	ControlKey    string
	ControlKeyID  string
	APIURL        string
	UserID        string
	Email         string
	Name          string
	WorkspaceID   string
	WorkspaceName string
	LoggedInAt    string
}

// LoggedIn reports whether the credential has an account control key.
func (a Account) LoggedIn() bool { return a.ControlKey != "" }

// AccountFromMap reads the signed-in account from an auth file's fields.
func AccountFromMap(fields map[string]any) Account {
	return Account{
		ControlKey:    jsonfield.String(fields, KeyAccountControlKey),
		ControlKeyID:  jsonfield.String(fields, KeyAccountControlKeyID),
		APIURL:        jsonfield.String(fields, KeyAccountAPIURL),
		UserID:        jsonfield.String(fields, KeyAccountUserID),
		Email:         jsonfield.String(fields, KeyAccountEmail),
		Name:          jsonfield.String(fields, KeyAccountName),
		WorkspaceID:   jsonfield.String(fields, KeyAccountWorkspaceID),
		WorkspaceName: jsonfield.String(fields, KeyAccountWorkspaceName),
		LoggedInAt:    jsonfield.String(fields, KeyAccountLoggedInAt),
	}
}

// accountOf is the account signed in on a credential.
func accountOf(cred credential.Credential) Account {
	return AccountFromMap(cred.Fields)
}

// findAuth finds a credential by auth index ("" matches none).
func findAuth(files []provider.AuthFile, authIndex string) (provider.AuthFile, bool) {
	authIndex = strings.TrimSpace(authIndex)
	for _, file := range files {
		if authIndex != "" && file.AuthIndex == authIndex {
			return file, true
		}
	}
	return provider.AuthFile{}, false
}

func cloneFields(fields map[string]any) map[string]any {
	out := make(map[string]any, len(fields)+1)
	for key, value := range fields {
		out[key] = value
	}
	return out
}

func setOrDelete(fields map[string]any, key, value string) {
	if value == "" {
		delete(fields, key)
		return
	}
	fields[key] = value
}

// jsonResponse and errorBody are the management answers of the account
// routes and the card figures.
var jsonResponse = provider.JSONResponse

type errorBody = provider.ErrorBody
