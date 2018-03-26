package exchange

import (
	"context"
	"errors"
	"fmt"
	"math"
	"pumpdump/helper"
	"time"

	binanceLib "github.com/anhpha/go-binance"
)

type binance struct {
	key         string
	secret      string
	worker      *binanceLib.Client
	exchageInfo binanceLib.ExchangeInfo
}

const (
	LOT_SIZE     = "LOT_SIZE"
	PRICE_FILTER = "PRICE_FILTER"
)

// NewBinance init a binance instance
func NewBinance(key string, secret string) Market {
	return &binance{
		key:    key,
		secret: secret,
		worker: binanceLib.NewClient(key, secret),
	}
}

func (b *binance) GetExchangeInfo() (interface{}, error) {
	infos, err := b.worker.NewExchangeInfoService().Do(context.Background())
	if err != nil {
		b.exchageInfo = *infos
		return infos, nil
	}
	return nil, err
}

func (b *binance) GetPairInfo(pair string) (Pair, error) {
	var result Pair
	for _, s := range b.exchageInfo.Symbols {
		if s.Symbol == pair {
			result = Pair{Symbol: pair}
			for _, filter := range s.Filters {
				switch filter["filterType"] {
				case "PRICE_FILTER":
					result.PriceFilter = PriceFilter{
						Min:  helper.StringToFloat64(filter["minPrice"]),
						Max:  helper.StringToFloat64(filter["maxPrice"]),
						Tick: helper.StringToFloat64(filter["tickSize"]),
					}
				case "LOT_SIZE":
					result.LotSize = LotSize{
						Min:  helper.StringToFloat64(filter["minQty"]),
						Max:  helper.StringToFloat64(filter["maxQty"]),
						Step: helper.StringToFloat64(filter["stepSize"]),
					}
				case "MIN_NOTIONAL":
					result.MinNotional = MinNotional{Value: helper.StringToFloat64(filter["minNotional"])}
				}
			}
			return result, nil
		}
	}
	return result, errors.New("Not found")
}

func (b *binance) GetPairCurrentBestPrice(pair string) (BestPrice, error) {
	var result BestPrice
	data, err := b.worker.NewBookTickerService().Symbol(pair).Do(context.Background())
	if err == nil {
		result.Symbol = pair
		result.Ask = helper.StringToFloat64(data.AskQuantity)
		result.AskPrice = helper.StringToFloat64(data.AskPrice)
		result.Bid = helper.StringToFloat64(data.BidQuantity)
		result.BidPrice = helper.StringToFloat64(data.BidPrice)
	}
	return result, err
}

func (b *binance) tryToGetCurrentBestPrice(pair string, maxTry int, delay int) (BestPrice, error) {
	var result BestPrice
	tried := 0
	done := 0
	for done == 0 && tried < maxTry {
		p, err := b.GetPairCurrentBestPrice(pair)
		if err == nil {
			done = 1
			return p, err
		}
	}
	return result, fmt.Errorf("Cannot get current best price for %s", pair)
}

func (b *binance) Fomo(pair string, amount float64, maxPrice float64, tk float64, sl float64, delay int, c chan error) error {
	var openOderID int64
	openOderID = 0
	done := 0

	pairInfo, err := b.GetPairInfo(pair)
	if err != nil {
		fmt.Printf("Cannot get symbol info %s", err)
		c <- fmt.Errorf("Cannot get pair info: %v", err)
		return err
	}

	marketSellPrice, err := b.tryToGetCurrentBestPrice(pair, 10, 200)
	if err != nil {
		fmt.Printf("Cannot get symbol info %s", err)
		c <- err
		return err
	}
	// Default will not buy if price increase more than 1%
	if maxPrice == 0 {
		maxPrice = marketSellPrice.AskPrice * 1.1
	}

	maxByPrice := amount / marketSellPrice.BidPrice

	willBuyAmout := maxByPrice - math.Mod(maxByPrice, pairInfo.LotSize.Step)
	hasError := 0
	needToBuyMore := 1

	for done != 1 {
		if openOderID > 0 {
			openOrder, err := b.GetOrderInfo(pair, openOderID, 100)
			// Stop loop with error
			if err == nil {
				hasError = 0
				// Done
				if openOrder.Status == "FILLED" {
					done = 1
					needToBuyMore = 0
				} else {
					needToBuyMore = 1
				}
				// Continue to buy until filled
				if openOrder.Status == "PARTIALLY_FILLED" {
					willBuyAmout = willBuyAmout - helper.StringToFloat64(openOrder.ExecutedQuantity)
				}
				// Cancel current "open" order
				if openOrder.Status == "NEW" || openOrder.Status == "PARTIALLY_FILLED" {
					checkingPrice, err := b.GetPairCurrentBestPrice(pair)
					// Cancel order if is not the best price
					if err == nil && checkingPrice.BidPrice > helper.StringToFloat64(openOrder.Price) {
						needToBuyMore = 1
						err = b.tryToCancelOrder(pair, openOrder.OrderID, 100)
						if err != nil {
							hasError = 1
						} else {
							hasError = 0
						}
					} else {
						hasError = 1
					}
				}
			} else {
				hasError = 1
			}
		}
		if hasError != 1 && needToBuyMore == 1 && willBuyAmout >= pairInfo.LotSize.Min {
			newOrder, err := b.tryToBuyBestByMarket(pair, willBuyAmout, maxPrice, delay)
			if err == nil {
				fmt.Printf("Order success: %v\n", newOrder)
				openOderID = newOrder.OrderID
				// Set take profit and stoploss
				go b.TryToSetTakeProfitAndStopLost(pair, helper.StringToFloat64(newOrder.ExecutedQuantity), tk, sl, 100, delay)
			}
		}
		// try again after 0.2 s
		wait := time.Duration(delay) * time.Millisecond
		// try again after 0.2 s
		time.Sleep(wait)
	}
	return nil
}

func (b *binance) TryToSetTakeProfitAndStopLost(pair string, amount float64, profitPrice float64, lossPrice float64, maxTry int, delay int) error {
	done := 0
	tried := 0
	for done != 1 && tried <= maxTry {
		profitOrder, err := b.createLimitOrder(binanceLib.SideTypeBuy, binanceLib.OrderTypeLimit, pair, helper.Float64ToString(profitPrice), helper.Float64ToString(amount))
		if err == nil {
			fmt.Printf("Will take profit with %v\n", profitOrder)
			b.MonitorAndStopLoss(pair, profitOrder, lossPrice, delay)
			done = 1
			return nil
		}
		tried++
	}
	return nil
}

func (b *binance) MonitorAndStopLoss(pair string, order *binanceLib.CreateOrderResponse, stopPrice float64, interval int) error {
	fmt.Printf("Motitoring pair: %s\n", pair)
	done := 0

	needToStoploss := 0
	needCancelOldOrder := 1

	for done != 1 {
		currentOrder, err := b.GetOrderInfo(pair, order.OrderID, 100)
		if err == nil && currentOrder.Status == "FILLED" {
			fmt.Printf("*** Taken profit @ %v\n", currentOrder.Price)
			done = 1
			return nil
		}
		// Check price
		if needToStoploss == 0 {
			bestPrice, err := b.GetPairCurrentBestPrice(pair)
			if err == nil && bestPrice.AskPrice <= stopPrice {
				needToStoploss = 1

			}
		}
		if needToStoploss == 1 {
			// Cancel open order
			if needCancelOldOrder == 1 {
				err := b.tryToCancelOrder(pair, order.OrderID, 100)
				if err == nil {
					needCancelOldOrder = 0

				}
			}
			// Try to sell at market price
			slOrder, err := b.worker.NewCreateOrderService().Symbol(pair).
				Side(binanceLib.SideTypeSell).Type(binanceLib.OrderTypeMarket).Quantity(order.ExecutedQuantity).Do(context.Background())
			if err == nil {
				done = 1
				fmt.Printf(":(((( Stoploss @ %v\n", slOrder)
				return nil
			}
		}
		wait := time.Duration(interval) * time.Millisecond
		// try again after 0.2 s
		time.Sleep(wait)
	}
	return fmt.Errorf("Cannot do stoploss for %v", order)
}

func (b *binance) GetOrderInfo(pair string, orderID int64, maxTry int) (*binanceLib.Order, error) {
	// var result binanceLib.Order
	ok := 0
	tried := 0
	for ok == 0 && tried <= maxTry {
		order, err := b.worker.NewGetOrderService().Symbol(pair).OrderID(orderID).Do(context.Background())
		if err == nil {
			ok = 1
			return order, nil
		}
		// try again after 0.2 s
		wait := 200 * time.Millisecond
		// try again after 0.2 s
		time.Sleep(wait)
		tried++
	}
	return nil, errors.New("Cannot get order status")
}

func (b *binance) tryToBuyBestByMarket(pair string, amount float64, maxPrice float64, interval int) (*binanceLib.CreateOrderResponse, error) {
	ok := 0
	for ok == 0 {
		pairInfo, err := b.GetPairInfo(pair)
		if err == nil {
			bestPrice, err := b.GetPairCurrentBestPrice(pair)
			if err == nil {
				tryPrice := bestPrice.Bid + pairInfo.PriceFilter.Tick
				if tryPrice > maxPrice {
					ok = -1
					return nil, errors.New("Price too high")
				}
				ok = 1
				return b.createLimitOrder(binanceLib.SideTypeBuy, binanceLib.OrderTypeLimit, pair, helper.Float64ToString(tryPrice), helper.Float64ToString(amount))
			}
		}
		// try again after 0.2 s
		wait := time.Duration(interval) * time.Millisecond
		// try again after 0.2 s
		time.Sleep(wait)
	}
	return nil, errors.New("Cannot put new order")
}

func (b *binance) tryToCancelOrder(pair string, orderID int64, maxTry int) error {
	ok := 0
	tried := 0
	for ok == 0 && tried <= maxTry {
		_, err := b.worker.NewCancelOrderService().Symbol(pair).OrderID(orderID).Do(context.Background())
		if err == nil {
			ok = 1
			return nil
		}
		// try again after 0.2 s
		wait := 200 * time.Millisecond
		// try again after 0.2 s
		time.Sleep(wait)
		tried++
	}
	return fmt.Errorf("Cannot cancel order %d after %d times", orderID, tried)
}

func (b *binance) createLimitOrder(orderSide binanceLib.SideType, orderType binanceLib.OrderType, pair string, price string, quantity string) (*binanceLib.CreateOrderResponse, error) {
	s := b.worker.NewCreateOrderService().Symbol(pair).
		Side(orderSide).Type(orderType).Quantity(quantity)

	if orderType == "STOP_LOSS_LIMIT" || orderType == "TAKE_PROFIT_LIMIT" || orderType == "LIMIT" {
		s = s.TimeInForce(binanceLib.TimeInForceGTC).Price(price)
	}
	return s.Do(context.Background())
}
