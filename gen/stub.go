package gen

import "errors"

type ChannelInfo struct {
	ID           string
	Name         string
	IsPrivate    bool
	AllowedRoles []string
}

type RoleInfo struct {
	ID   string
	Name string
}

var (
	channels    map[string]ChannelInfo
	roles       map[string]RoleInfo
	channelsErr = errors.New("not implemented")
)

// Channels returns the generated channel mapping.
// Before generation, it returns "not implemented" error.
func Channels() (map[string]ChannelInfo, error) {
	if channelsErr != nil {
		return nil, channelsErr
	}
	return channels, nil
}

// Roles returns the generated role mapping.
// Before generation, it returns "not implemented" error.
func Roles() (map[string]RoleInfo, error) {
	if channelsErr != nil {
		return nil, channelsErr
	}
	return roles, nil
}
