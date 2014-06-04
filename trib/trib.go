// Package trib defines basic interfaces and constants
// for Tribbler service implementation.
package trib

import (
	"time"
)

const (
	MaxUsernameLen = 15   // Maximum length of a username
	MaxTribLen     = 140  // Maximum length of a tribble
	MaxTribFetch   = 100  // Maximum count of tribbles for Home() and Tribs()
	MinListUser    = 20   // Minimum count of users required for ListUsers()
	MaxFollowing   = 2000 // Maximum count of users that one can follow
)

type Trib struct {
	User    string    // who posted this trib
	Message string    // the content of the trib
	Time    time.Time // the physical timestamp
	Clock   uint64    // the logical timestamp
}

type Server interface {
	// Creates a user.
	// Returns error when the username is invalid;
	// returns error when the user already exists.
	// Concurrent sign ups on the same user might both succeed with no error.
	SignUp(user string) error

	// List 20 registered users.  When there are less than 20 users that
	// signed up the service, all of them needs to be listed.  When there
	// are more than 20 users that signed up the service, an arbitrary set
	// of at lest 20 of them needs to be listed.
	// The result should be sorted in alphabetical order.
	ListUsers() ([]string, error)

	// Post a tribble.  The clock is the maximum clock value this user has
	// seen so far by reading tribbles or clock sync.
	// Returns error when who does not exist;
	// returns error when post is too long.
	Post(who, post string, clock uint64) error

	// List the tribs that a particular user posted.
	// Returns error when user has not signed up.
	Tribs(user string) ([]*Trib, error)

	// Follow someone's timeline.
	// Returns error when who == whom;
	// returns error when who is already following whom;
	// returns error when who is tryting to following
	// more than trib.MaxFollowing users.
	// returns error when who or whom has not signed up.
	Follow(who, whom string) error

	// Unfollow someone's timeline.
	// Returns error when who == whom.
	// returns error when who is not following whom;
	// returns error when who or whom has not signed up.
	Unfollow(who, whom string) error

	// Returns true when who following whom.
	// Returns error when who == whom.
	// Returns error when who or whom has not signed up.
	IsFollowing(who, whom string) (bool, error)

	// Returns the list of following users.
	// Returns error when who has not signed up.
	Following(who string) ([]string, error)

	// List the tribs of someone's following users (including himself).
	// Returns error when user has not signed up.
	Home(user string) ([]*Trib, error)
}

// Checks if a username is a valid one. Returns true if it is.
func IsValidUsername(s string) bool {
	if s == "" {
		return false
	}

	if len(s) > MaxUsernameLen {
		return false
	}

	for i, r := range s {
		if r >= 'a' && r <= 'z' {
			continue
		}

		if i > 0 && r >= '0' && r <= '9' {
			continue
		}

		return false
	}

	return true
}
