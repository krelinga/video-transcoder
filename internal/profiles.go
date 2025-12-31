package internal

import "errors"

type Profile string

const ProfilePreview Profile = "preview"
const ProfileFast1080p30 Profile = "fast1080p30"

var ErrPanicInvalidProfile = errors.New("invalid profile")

func (p Profile) IsValid() bool {
	switch p {
	case ProfilePreview, ProfileFast1080p30:
		return true
	default:
		return false
	}
}
