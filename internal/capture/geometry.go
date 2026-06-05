package capture

import "image"

func UnionDisplays(displays []image.Rectangle) image.Rectangle {
	if len(displays) == 0 {
		return image.Rectangle{}
	}

	union := displays[0]
	for _, display := range displays[1:] {
		union = union.Union(display)
	}
	return union
}
