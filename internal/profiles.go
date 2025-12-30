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

type ProfileSwitch struct {
	Preview func() error
}

func (ps ProfileSwitch) Switch(p Profile) error {
	switch p {
	case ProfilePreview:
		return ps.Preview()
	default:
		panic(ErrPanicInvalidProfile)
	}
}
