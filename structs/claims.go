package structs

import "github.com/dgrijalva/jwt-go"

type CustomClaims struct {
	User     string   `json:"user"`
	ID       int64    `json:"id"`
	Roles    []string `json:"roles"`
	Email    string   `json:"email,omitempty"`
	Name     string   `json:"name,omitempty"`
	Lastname string   `json:"last_name,omitempty"`
	ImageURL string   `json:"image_url,omitempty"`
	jwt.StandardClaims
}
