package netease

import "encoding/json"

// Adapted from nethard-core src/game/g79/client.ts default sauthData.
// Source: https://github.com/nethard-project/nethard-core-channel
func buildSAData(sauth json.RawMessage) (string, error) {
	var account struct {
		AppChannel string `json:"app_channel"`
		UDID       string `json:"udid"`
	}
	if err := json.Unmarshal(sauth, &account); err != nil {
		return "", err
	}
	data, err := json.Marshal(map[string]any{
		"app_channel": account.AppChannel, "app_ver": PatchVersion,
		"core_num": "8", "cpu_digit": "64", "cpu_hz": "1804800", "cpu_name": "",
		"device_height": "2712", "device_model": "XIAOMI 22081212C", "device_width": "1220",
		"disk": "", "emulator": 0, "first_udid": account.UDID, "is_guest": 0,
		"launcher_type": "PE_C++", "mac_addr": "00:00:00:00:00:00",
		"network": "CHANNEL_UNKNOW", "os_name": "android", "os_ver": "13",
		"ram": "11718889472", "rom": "240090329088", "root": false,
		"sdk_ver": "5.16.0", "start_type": "defualt", "udid": account.UDID,
	})
	return string(data) + "\n", err
}
