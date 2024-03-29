package customresource

import "time"

// Payload represents the data sent as the payload of an event.
type Payload struct {
	Type         string      `json:"type"`
	Token        string      `json:"token"`
	TokenExpires time.Time   `json:"tokenExpires"`
	Body         interface{} `json:"body"`
	AppID        int         `json:"-"`
	InstID       int         `json:"-"`
	Commit       string      `json:"commit"`
	Branch       string      `json:"branch"`

	Owner   string `json:"owner"`
	Repo    string `json:"repo"`
	Pull    string `json:"pull"`
	PullURL string `json:"pullURL"`
}

