package drivers

import (
	"net/http"

	"github.com/fullteam-tech/gocialite/structs"
	"golang.org/x/oauth2"
)

const lineDriverName = "line"

func init() {
	lineEndpoint := oauth2.Endpoint{
		AuthURL:   "https://api.line.me/v2/oauth/accessToken",
		TokenURL:  "https://api.line.me/oauth2/v2.1/verify",
		AuthStyle: oauth2.AuthStyleInParams,
	}
	registerDriver(lineDriverName, LineDefaultScopes, LineUserFn, lineEndpoint, LineAPIMap, LineUserMap)
}

// LineUserMap is the map to create the User struct
var LineUserMap = map[string]string{
	"sub":         "ID",
	"email":       "Email",
	"name":        "FullName",
	"given_name":  "FirstName",
	"family_name": "LastName",
	"picture":     "Avatar",
}

// LineAPIMap is the map for API endpoints
var LineAPIMap = map[string]string{
	"endpoint":     "https://api.line.me",
	"userEndpoint": "/oauth2/v2.1/verify",
}

// LineUserFn is a callback to parse additional fields for User
var LineUserFn = func(client *http.Client, u *structs.User) {}

// LineDefaultScopes contains the default scopes
var LineDefaultScopes = []string{"profile", "email"}
