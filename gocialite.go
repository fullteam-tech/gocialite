package gocialite

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io/ioutil"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/dgrijalva/jwt-go"
	"github.com/fullteam-tech/gocialite/drivers"
	"github.com/fullteam-tech/gocialite/structs"
	"github.com/s12v/go-jwks"
	"golang.org/x/oauth2"
	"gopkg.in/oleiade/reflections.v1"
)

var jwksAppleClient jwks.JWKSClient

func init() {
	jwksSource := jwks.NewWebSource("https://appleid.apple.com/auth/keys")
	jwksAppleClient = jwks.NewDefaultClient(
		jwksSource,
		time.Minute*10,
		12*time.Hour,
	)
}

// Dispatcher allows to safely issue concurrent Gocials
type Dispatcher struct {
	mu sync.RWMutex
	g  map[string]*Gocial
	gt *Gocial
}

// NewDispatcher creates new Dispatcher
func NewDispatcher() *Dispatcher {
	return &Dispatcher{g: make(map[string]*Gocial), gt: &Gocial{}}
}

// New Gocial instance
func (d *Dispatcher) New() *Gocial {
	d.mu.Lock()
	defer d.mu.Unlock()
	state := randToken()
	g := &Gocial{state: state}
	d.g[state] = g

	return g
}

// Handle callback. Can be called only once for given state.
func (d *Dispatcher) Handle(state, code string) (*structs.User, *oauth2.Token, error) {
	d.mu.RLock()
	g, ok := d.g[state]
	d.mu.RUnlock()
	if !ok {
		return nil, nil, fmt.Errorf("invalid CSRF token: %s", state)
	}
	err := g.Handle(state, code)
	d.mu.Lock()
	delete(d.g, state)
	d.mu.Unlock()
	return &g.User, g.Token, err
}

// HandleToken get user profiel
func (d *Dispatcher) HandleToken(provider string, token string) (*structs.User, error) {
	if provider == "apple" {
		user, err := d.gt.HandleAppleToken(token)
		return user, err
	}
	user, err := d.gt.HandleToken(provider, token)
	return user, err
}

// Gocial is the main struct of the package
type Gocial struct {
	driver, state string
	scopes        []string
	conf          *oauth2.Config
	User          structs.User
	Token         *oauth2.Token
}

func init() {
	drivers.InitializeDrivers(RegisterNewDriver)
}

var (
	// Set the basic information such as the endpoint and the scopes URIs
	apiMap = map[string]map[string]string{}

	// Mapping to create a valid "user" struct from providers
	userMap = map[string]map[string]string{}

	// Map correct endpoints
	endpointMap = map[string]oauth2.Endpoint{}

	// Map custom callbacks
	callbackMap = map[string]func(client *http.Client, u *structs.User){}

	// Default scopes for each driver
	defaultScopesMap = map[string][]string{}
)

//RegisterNewDriver adds a new driver to the existing set
func RegisterNewDriver(driver string, defaultscopes []string, callback func(client *http.Client, u *structs.User), endpoint oauth2.Endpoint, apimap, usermap map[string]string) {
	apiMap[driver] = apimap
	userMap[driver] = usermap
	endpointMap[driver] = endpoint
	callbackMap[driver] = callback
	defaultScopesMap[driver] = defaultscopes
}

// Driver is needed to choose the correct social
func (g *Gocial) Driver(driver string) *Gocial {
	g.driver = driver
	g.scopes = defaultScopesMap[driver]

	// BUG: sequential usage of single Gocial instance will have same CSRF token. This is serious security issue.
	// NOTE: Dispatcher eliminates this bug.
	if g.state == "" {
		g.state = randToken()
	}

	return g
}

// Scopes is used to set the oAuth scopes, for example "user", "calendar"
func (g *Gocial) Scopes(scopes []string) *Gocial {
	g.scopes = append(g.scopes, scopes...)
	return g
}

// Redirect returns an URL for the selected social oAuth login
func (g *Gocial) Redirect(clientID, clientSecret, redirectURL string) (string, error) {
	// Check if driver is valid
	if !inSlice(g.driver, complexKeys(apiMap)) {
		return "", fmt.Errorf("Driver not valid: %s", g.driver)
	}

	// Check if valid redirectURL
	_, err := url.ParseRequestURI(redirectURL)
	if err != nil {
		return "", fmt.Errorf("Redirect URL <%s> not valid: %s", redirectURL, err.Error())
	}
	if !strings.HasPrefix(redirectURL, "http://") && !strings.HasPrefix(redirectURL, "https://") {
		return "", fmt.Errorf("Redirect URL <%s> not valid: protocol not valid", redirectURL)
	}

	g.conf = &oauth2.Config{
		ClientID:     clientID,
		ClientSecret: clientSecret,
		RedirectURL:  redirectURL,
		Scopes:       g.scopes,
		Endpoint:     endpointMap[g.driver],
	}

	return g.conf.AuthCodeURL(g.state), nil
}

// Handle callback from provider
func (g *Gocial) Handle(state, code string) error {
	// Handle the exchange code to initiate a transport.
	if g.state != state {
		return fmt.Errorf("Invalid state: %s", state)
	}

	// Check if driver is valid
	if !inSlice(g.driver, complexKeys(apiMap)) {
		return fmt.Errorf("Driver not valid: %s", g.driver)
	}

	token, err := g.conf.Exchange(oauth2.NoContext, code)
	if err != nil {
		return fmt.Errorf("oAuth exchanged failed: %s", err.Error())
	}

	client := g.conf.Client(oauth2.NoContext, token)

	// Set gocial token
	g.Token = token

	// Retrieve all from scopes
	driverAPIMap := apiMap[g.driver]
	driverUserMap := userMap[g.driver]
	userEndpoint := strings.Replace(driverAPIMap["userEndpoint"], "%ACCESS_TOKEN", token.AccessToken, -1)

	// Get user info
	req, err := client.Get(driverAPIMap["endpoint"] + userEndpoint)
	if err != nil {
		return err
	}

	defer req.Body.Close()
	res, _ := ioutil.ReadAll(req.Body)
	data, err := jsonDecode(res)
	if err != nil {
		return fmt.Errorf("Error decoding JSON: %s", err.Error())
	}

	// Scan all fields and dispatch through the mapping
	mapKeys := keys(driverUserMap)
	gUser := structs.User{}
	for k, f := range data {
		if !inSlice(k, mapKeys) { // Skip if not in the mapping
			continue
		}

		// Assign the value
		// Dirty way, but we need to convert also int/float to string
		_ = reflections.SetField(&gUser, driverUserMap[k], fmt.Sprint(f))
	}

	// Set the "raw" user interface
	gUser.Raw = data

	// Custom callback
	callbackMap[g.driver](client, &gUser)

	// Update the struct
	g.User = gUser

	return nil
}

// HandleToken get user profile from token
func (g *Gocial) HandleToken(provider string, token string) (*structs.User, error) {
	// Retrieve all from scopes
	if _, ok := apiMap[provider]; !ok {
		return nil, errors.New("Provider not found")
	}
	driverAPIMap := apiMap[provider]
	driverUserMap := userMap[provider]
	userEndpoint := strings.Replace(driverAPIMap["userEndpoint"], "%ACCESS_TOKEN", token, -1)
	// Get user info
	g.User = structs.User{}
	req, err := http.NewRequest("GET", driverAPIMap["endpoint"]+userEndpoint, nil) // , bytes.NewBuffer(jsonStr)
	q := req.URL.Query()                                                           // Get a copy of the query values.
	if provider == "google" {
		q.Add("id_token", token)
	} else if provider == "line" {
		payload := strings.NewReader(fmt.Sprintf("client_id=%s&id_token=%s", os.Getenv("LINE_CLIENT_ID"), token))
		req, err = http.NewRequest("POST", driverAPIMap["endpoint"]+userEndpoint, payload)
		req.Header.Add("Content-Type", "application/x-www-form-urlencoded")
	} else {
		q.Add("access_token", token)
	}
	req.URL.RawQuery = q.Encode()

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}

	if resp.StatusCode >= 200 && resp.StatusCode <= 299 {
		defer resp.Body.Close()

		body, _ := ioutil.ReadAll(resp.Body)
		data, err := jsonDecode(body)
		if err, ok := data["error"]; ok {
			errorDetail := err.(map[string]interface{})
			if errorMessage, ok := errorDetail["message"]; ok {
				return nil, errors.New(errorMessage.(string))
			}
			return nil, errors.New("Token is invalid")
		}
		if err != nil {
			return nil, fmt.Errorf("Error decoding JSON: %s", err.Error())
		}

		// Scan all fields and dispatch through the mapping
		mapKeys := keys(driverUserMap)
		gUser := structs.User{}
		for k, f := range data {
			if !inSlice(k, mapKeys) { // Skip if not in the mapping
				continue
			}

			// Assign the value
			// Dirty way, but we need to convert also int/float to string
			_ = reflections.SetField(&gUser, driverUserMap[k], fmt.Sprint(f))
		}

		// Set the "raw" user interface
		gUser.Raw = data

		// Update the struct
		return &gUser, nil
	} else {
		return nil, errors.New("Token is invalid")
	}
}

func (g *Gocial) HandleAppleToken(idToken string) (*structs.User, error) {
	token, err := jwt.ParseWithClaims(idToken, &structs.CustomClaims{}, func(token *jwt.Token) (interface{}, error) {
		jwk, err := jwksAppleClient.GetEncryptionKey(fmt.Sprintf("%v", token.Header["kid"]))
		if err != nil {
			return nil, err
		}
		return jwk.Key, nil
	})
	if err != nil {
		return nil, err
	}
	if !token.Valid {
		return nil, err
	}
	user := token.Claims.(*structs.CustomClaims)

	if "https://appleid.apple.com" != user.Issuer {
		return nil, errors.New("token is invalid")
	}

	if os.Getenv("APPLE_CLIENT_ID") != user.Audience {
		err := errors.New("token is invalid")
		return nil, err
	}

	u := structs.User{
		ID:        user.Subject,
		Username:  "",
		FirstName: user.Name,
		LastName:  user.Lastname,
		FullName:  user.Name + " " + user.Lastname,
		Email:     user.Email,
		// Avatar
		// Raw
	}

	return &u, nil
}

// Generate a random token
func randToken() string {
	b := make([]byte, 32)
	rand.Read(b)
	return base64.StdEncoding.EncodeToString(b)
}

// Check if a value is in a string slice
func inSlice(v string, s []string) bool {
	for _, scope := range s {
		if scope == v {
			return true
		}
	}

	return false
}

// Decode a json or return an error
func jsonDecode(js []byte) (map[string]interface{}, error) {
	var decoded map[string]interface{}
	decoder := json.NewDecoder(strings.NewReader(string(js)))
	decoder.UseNumber()

	if err := decoder.Decode(&decoded); err != nil {
		return nil, err
	}

	return decoded, nil
}

// Return the keys of a map
func keys(m map[string]string) []string {
	var keys []string
	for k := range m {
		keys = append(keys, k)
	}

	return keys
}

func complexKeys(m map[string]map[string]string) []string {
	var keys []string
	for k := range m {
		keys = append(keys, k)
	}

	return keys
}
