package indexer

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/big"
	"strings"
)

const (
	topicJobFunded         = "0x8220b978cac568b980751c54df59af3be6c1d3bd9874232210cc1cf89740142b"
	topicAdvanceIssued     = "0x4e000615bb000c437ff360e4f54ea1722dc46e202857ff124e0668f955301da7"
	topicExpenseRecorded   = "0x016b8b2be22dbb474f61e8f543dbe04e2df3bd99a078612b73b1c553063a92de"
	topicDeliverySubmitted = "0xad8578136a5b42ae9e2a5cbc2743365e76b284462cd7273c9b8afa548b62a68a"
	topicJobSettled        = "0xd279a21adfe210809eb47e25b172b6b8c49f3c909ca3fe860c28ad1670ed6680"
	topicAdvanceRepaid     = "0xb1a154c78bda0dfbf33f2c572b5d8ce519a400aa92b38315e90daa26e44f1b4c"
	topicAdvanceWrittenOff = "0x6a6428d29409279a788a4399a8204370bd90228631e24511ba87073e9f65a48f"
	topicRateChanged       = "0xec739c9af710a6df2b3e3656f38b5d59af57d3022cd5a88ca4516db96a4ca5c7"
)

type decodedEvent struct {
	Kind     string
	EntityID string
	Actor    string
	Payload  map[string]string
}

func (e decodedEvent) payloadJSON() (string, error) {
	raw, err := json.Marshal(e.Payload)
	return string(raw), err
}

// decode implements the eight-event H1 freeze. Unknown topics remain durable in chain_logs but
// intentionally produce no normalized event; known topics with malformed ABI data fail closed.
func decode(log Log) (decodedEvent, bool, error) {
	if len(log.Topics) == 0 {
		return decodedEvent{}, false, nil
	}
	topic0 := strings.ToLower(log.Topics[0])
	words, err := dataWords(log.Data)
	if err != nil {
		if isH1Topic(topic0) {
			return decodedEvent{}, false, fmt.Errorf("%s data: %w", topic0, err)
		}
		return decodedEvent{}, false, nil
	}

	switch topic0 {
	case topicJobFunded:
		jobID, err := indexedBytes32(log.Topics, 1)
		if err != nil || len(words) != 1 {
			return decodedEvent{}, false, abiShape("JobFunded", err, len(words), 1)
		}
		return decodedEvent{"JobFunded", jobID, "", map[string]string{"amountAtomic": uintWord(words[0])}}, true, nil

	case topicAdvanceIssued:
		jobID, err := indexedBytes32(log.Topics, 1)
		if err != nil {
			return decodedEvent{}, false, abiShape("AdvanceIssued", err, len(words), 3)
		}
		org, err := indexedAddress(log.Topics, 2)
		if err != nil || len(words) != 3 {
			return decodedEvent{}, false, abiShape("AdvanceIssued", err, len(words), 3)
		}
		rate, err := uint64Word(words[2])
		if err != nil || rate > 10_000 {
			return decodedEvent{}, false, fmt.Errorf("AdvanceIssued rateBps is invalid: %d (%v)", rate, err)
		}
		return decodedEvent{"AdvanceIssued", jobID, org, map[string]string{
			"org": org, "principalAtomic": uintWord(words[0]), "feeAtomic": uintWord(words[1]), "rateBps": fmt.Sprint(rate),
		}}, true, nil

	case topicExpenseRecorded:
		jobID, err := indexedBytes32(log.Topics, 1)
		if err != nil || len(words) != 2 {
			return decodedEvent{}, false, abiShape("ExpenseRecorded", err, len(words), 2)
		}
		return decodedEvent{"ExpenseRecorded", jobID, "", map[string]string{
			"amountAtomic": uintWord(words[0]), "receiptHash": bytes32Word(words[1]),
		}}, true, nil

	case topicDeliverySubmitted:
		jobID, err := indexedBytes32(log.Topics, 1)
		if err != nil || len(words) != 1 {
			return decodedEvent{}, false, abiShape("DeliverySubmitted", err, len(words), 1)
		}
		return decodedEvent{"DeliverySubmitted", jobID, "", map[string]string{"deliveryHash": bytes32Word(words[0])}}, true, nil

	case topicJobSettled:
		jobID, err := indexedBytes32(log.Topics, 1)
		if err != nil || len(words) != 2 {
			return decodedEvent{}, false, abiShape("JobSettled", err, len(words), 2)
		}
		return decodedEvent{"JobSettled", jobID, "", map[string]string{
			"advanceRepaidAtomic": uintWord(words[0]), "operatorNetAtomic": uintWord(words[1]),
		}}, true, nil

	case topicAdvanceRepaid:
		jobID, err := indexedBytes32(log.Topics, 1)
		if err != nil || len(words) != 3 {
			return decodedEvent{}, false, abiShape("AdvanceRepaid", err, len(words), 3)
		}
		return decodedEvent{"AdvanceRepaid", jobID, "", map[string]string{
			"principalAtomic": uintWord(words[0]), "feeAtomic": uintWord(words[1]), "toReserveAtomic": uintWord(words[2]),
		}}, true, nil

	case topicAdvanceWrittenOff:
		jobID, err := indexedBytes32(log.Topics, 1)
		if err != nil || len(words) != 3 {
			return decodedEvent{}, false, abiShape("AdvanceWrittenOff", err, len(words), 3)
		}
		return decodedEvent{"AdvanceWrittenOff", jobID, "", map[string]string{
			"bondSlashedAtomic": uintWord(words[0]), "reserveUsedAtomic": uintWord(words[1]), "socializedAtomic": uintWord(words[2]),
		}}, true, nil

	case topicRateChanged:
		org, err := indexedAddress(log.Topics, 1)
		if err != nil || len(words) != 1 {
			return decodedEvent{}, false, abiShape("RateChanged", err, len(words), 1)
		}
		rate, err := uint64Word(words[0])
		if err != nil || rate > 10_000 {
			return decodedEvent{}, false, fmt.Errorf("RateChanged rateBps is invalid: %d (%v)", rate, err)
		}
		return decodedEvent{"RateChanged", org, org, map[string]string{"org": org, "rateBps": fmt.Sprint(rate)}}, true, nil
	}
	return decodedEvent{}, false, nil
}

func isH1Topic(topic string) bool {
	switch topic {
	case topicJobFunded, topicAdvanceIssued, topicExpenseRecorded, topicDeliverySubmitted,
		topicJobSettled, topicAdvanceRepaid, topicAdvanceWrittenOff, topicRateChanged:
		return true
	default:
		return false
	}
}

func dataWords(data string) ([][]byte, error) {
	data = strings.TrimPrefix(strings.ToLower(strings.TrimSpace(data)), "0x")
	if len(data)%64 != 0 {
		return nil, fmt.Errorf("ABI data has %d hex characters, not 32-byte words", len(data))
	}
	if data == "" {
		return nil, nil
	}
	raw, err := hex.DecodeString(data)
	if err != nil {
		return nil, fmt.Errorf("invalid hex: %w", err)
	}
	words := make([][]byte, 0, len(raw)/32)
	for len(raw) > 0 {
		words = append(words, raw[:32])
		raw = raw[32:]
	}
	return words, nil
}

func indexedBytes32(topics []string, index int) (string, error) {
	if len(topics) <= index {
		return "", fmt.Errorf("missing indexed topic %d", index)
	}
	value := strings.ToLower(topics[index])
	if len(value) != 66 || !strings.HasPrefix(value, "0x") {
		return "", fmt.Errorf("topic %d is not bytes32", index)
	}
	if _, err := hex.DecodeString(value[2:]); err != nil {
		return "", fmt.Errorf("topic %d is invalid hex: %w", index, err)
	}
	return value, nil
}

func indexedAddress(topics []string, index int) (string, error) {
	word, err := indexedBytes32(topics, index)
	if err != nil {
		return "", err
	}
	return normalizeAddress("0x" + word[len(word)-40:])
}

func uintWord(word []byte) string { return new(big.Int).SetBytes(word).String() }

func uint64Word(word []byte) (uint64, error) {
	n := new(big.Int).SetBytes(word)
	if !n.IsUint64() {
		return 0, fmt.Errorf("uint256 does not fit uint64")
	}
	return n.Uint64(), nil
}

func bytes32Word(word []byte) string { return "0x" + hex.EncodeToString(word) }

func abiShape(name string, prior error, got, want int) error {
	if prior != nil {
		return fmt.Errorf("%s topics: %w", name, prior)
	}
	return fmt.Errorf("%s has %d data words, want %d", name, got, want)
}
