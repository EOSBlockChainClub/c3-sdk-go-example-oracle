// Code generated by "stringer -type=supported"; DO NOT EDIT.

package chain

import "strconv"

const _supported_name = "ETHEREUMEOSIO"

var _supported_index = [...]uint8{0, 8, 13}

func (i supported) String() string {
	if i < 0 || i >= supported(len(_supported_index)-1) {
		return "supported(" + strconv.FormatInt(int64(i), 10) + ")"
	}
	return _supported_name[_supported_index[i]:_supported_index[i+1]]
}
