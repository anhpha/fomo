package exchange

// Market interface
type Market interface {
	GetExchangeInfo() (interface{}, error)
	GetPairInfo(symbol string) (Pair, error)
	Fomo(pair string, amount float64, buyPrice float64, maxPrice float64, tk float64, sl float64, race int, delay int, c chan error) (terminater chan error, e error)
	TryToStopLossForOpenOders(pair string, sl float64, delay int, terminater chan error) []error
	// NewInstance(key string, secret string) Market
}

// LotSize lot side of an exchange
type LotSize struct {
	Min  float64
	Max  float64
	Step float64
}

// PriceFilter lot side of an exchange
type PriceFilter struct {
	Min  float64
	Max  float64
	Tick float64
}

// MinNotional ..
type MinNotional struct {
	Value float64
}

// Pair ...
type Pair struct {
	Symbol      string
	LotSize     LotSize
	PriceFilter PriceFilter
	MinNotional MinNotional
}

// BestPrice ...
type BestPrice struct {
	Symbol   string
	Ask      float64
	Bid      float64
	AskPrice float64
	BidPrice float64
}
