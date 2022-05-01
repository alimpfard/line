package line

func ctrl(k rune) uint32 {
	return uint32(k & 0x3f)
}

type keyCallbackMachineImpl struct {
	keyCallbacks         map[uint32]KeybindingCallback
	keyAssignments       map[uint32][]key
	currentMatchingKeys  [][]key
	sequenceLength       int
	shouldProcessThisKey bool
}

var assignedKeyIndexSerial uint32 = 0

func newKeyCallbackMachine() keyCallbackMachine {
	return &keyCallbackMachineImpl{
		keyCallbacks:         make(map[uint32]KeybindingCallback),
		keyAssignments:       make(map[uint32][]key),
		currentMatchingKeys:  make([][]key, 0),
		sequenceLength:       0,
		shouldProcessThisKey: false,
	}
}

func (k *keyCallbackMachineImpl) registerInputCallback(keys []key, callback KeybindingCallback) {
	assignedIndex := k.findMatchingKeysIndex(keys)
	if assignedIndex == assignedKeyIndexSerial {
		assignedKeyIndexSerial++
	}

	k.keyAssignments[assignedIndex] = keys
	k.keyCallbacks[assignedIndex] = callback
}

func (k *keyCallbackMachineImpl) findMatchingKeysIndex(keys []key) uint32 {
	assignedIndex := assignedKeyIndexSerial
	for i, assignedKeys := range k.keyAssignments {
		if len(assignedKeys) == len(keys) {
			for j, key := range keys {
				if key != assignedKeys[j] {
					continue
				}
				if j == len(keys)-1 {
					assignedIndex = i
					break
				}
			}
		} else {
			continue
		}
	}
	return assignedIndex
}

func (k *keyCallbackMachineImpl) keyPressed(newKey key, editor Editor) {
	if k.sequenceLength == 0 {
		for i := range k.keyCallbacks {
			keys := k.keyAssignments[i]
			if keys[0] == newKey {
				k.currentMatchingKeys = append(k.currentMatchingKeys, keys)
			}
		}

		if len(k.currentMatchingKeys) == 0 {
			k.shouldProcessThisKey = true
			return
		}
	}

	k.sequenceLength++
	var oldMatchingKeys [][]key
	oldMatchingKeys = k.currentMatchingKeys
	k.currentMatchingKeys = nil

	for _, keys := range oldMatchingKeys {
		if len(keys) < k.sequenceLength {
			continue
		}
		if keys[k.sequenceLength-1] == newKey {
			k.currentMatchingKeys = append(k.currentMatchingKeys, keys)
		}
	}

	if len(k.currentMatchingKeys) == 0 {
		// Insert any keys that were captured
		if len(oldMatchingKeys) != 0 {
			keys := oldMatchingKeys[0]
			for i := 0; i < k.sequenceLength-1; i++ {
				editor.InsertChar(rune(keys[i].key))
			}
		}
		k.sequenceLength = 0
		k.shouldProcessThisKey = true
		return
	}

	k.shouldProcessThisKey = false
	for _, matchingKeys := range k.currentMatchingKeys {
		if len(matchingKeys) == k.sequenceLength {
			k.shouldProcessThisKey = k.keyCallbacks[k.findMatchingKeysIndex(matchingKeys)](matchingKeys, editor)
			k.sequenceLength = 0
			k.currentMatchingKeys = k.currentMatchingKeys[:0]
			return
		}
	}
}

func (k *keyCallbackMachineImpl) interrupted(editor Editor) {
	k.sequenceLength = 0
	k.currentMatchingKeys = k.currentMatchingKeys[:0]
	seq := []key{{key: ctrl('C')}}
	if index := k.findMatchingKeysIndex(seq); index != assignedKeyIndexSerial {
		k.shouldProcessThisKey = k.keyCallbacks[index](seq, editor)
	} else {
		k.shouldProcessThisKey = true
	}
}

func (k *keyCallbackMachineImpl) shouldProcessLastPressedKey() bool {
	return k.shouldProcessThisKey
}
