package internal

type Profile string

const ProfilePreview Profile = "preview"

var ErrPanicInvalidProfile = "invalid profile"

func (p Profile) IsValid() bool {
	switch p {
	case ProfilePreview:
		return true
	default:
		return false
	}
}

