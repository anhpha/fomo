package exchange

import (
	"context"
	"errors"
	"fmt"
	"log"
	"math"
	"net/http"
	"os"
	"pumpdump/helper"
	"time"

	binanceLib "github.com/adshao/go-binance"
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
		worker: &binanceLib.Client{
			APIKey:     key,
			SecretKey:  secret,
			BaseURL:    "https://api.binance.com",
			UserAgent:  "curl/7.47.0",
			HTTPClient: http.DefaultClient,
			Debug:      false,
			Logger:     log.New(os.Stdout, "Binance-golang ", log.LstdFlags),
		},
	}
}

func (b *binance) GetExchangeInfo() (interface{}, error) {
	infos, err := b.worker.NewExchangeInfoService().Do(context.Background())
	// fmt.Printf("Info: %v\n", infos)
	if err == nil {
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

func (b *binance) Fomo(pair string, amount float64, buyPrice float64, maxPrice float64, tk float64, sl float64, race int, delay int, c chan error) error {
	var openOderID int64
	openOderID = 0
	done := 0

	pairInfo, err := b.GetPairInfo(pair)

	// fmt.Printf("Will fomo on %v\n", pairInfo)
	// fmt.Printf("Min notional %v\n", pairInfo.MinNotional.Value)

	if err != nil {
		fmt.Printf("Cannot get symbol info %s\n", pair)
		c <- fmt.Errorf("Cannot get pair info: %v", err)
		return err
	}
	if buyPrice == 0 {
		marketSellPrice, err := b.tryToGetCurrentBestPrice(pair, 10, 200)
		if err != nil {
			fmt.Printf("Cannot get symbol info %s", err)
			c <- err
			return err
		}
		buyPrice = marketSellPrice.BidPrice
	} else {
		// Correct buy price
		buyPrice = buyPrice - math.Mod(buyPrice, pairInfo.PriceFilter.Tick)
	}
	// Default will not buy if price increase more than 1%
	if maxPrice == 0 {
		maxPrice = buyPrice * 1.1
	}

	maxByPrice := amount / buyPrice

	willBuyAmout := maxByPrice - math.Mod(maxByPrice, pairInfo.LotSize.Step)
	hasError := 0
	needToBuyMore := 1

	fmt.Printf("Will try to buy %v (%v USDT) @ %v\n", willBuyAmout, willBuyAmout*buyPrice, buyPrice)

	for done != 1 {

		if openOderID > 0 {
			openOrder, err := b.GetOrderInfo(pair, openOderID, 100)
			// fmt.Printf("Current opeing order %v @ %v: %v\n", openOrder.ExecutedQuantity, openOrder.Price, openOrder.Status)
			// Stop loop with error
			if err == nil {
				willBuyAmout = willBuyAmout - helper.StringToFloat64(openOrder.ExecutedQuantity)
				hasError = 0
				// Done
				if openOrder.Status == "FILLED" {
					done = 1
					needToBuyMore = 0
					// Set take profit and stoploss
					go b.TryToSetTakeProfitAndStopLost(pairInfo, helper.StringToFloat64(openOrder.Price), helper.StringToFloat64(openOrder.ExecutedQuantity), tk, sl, 100, delay, c)
				}
				// In case you want to race to buy
				// Cancel current "open" order
				if (openOrder.Status == "NEW" || openOrder.Status == "PARTIALLY_FILLED") && race == 1 {
					checkingPrice, err := b.GetPairCurrentBestPrice(pair)
					fmt.Printf("Current best bid price %v\n", checkingPrice.BidPrice)
					// Cancel order if is not the best price
					if err == nil && checkingPrice.BidPrice > helper.StringToFloat64(openOrder.Price) {
						// Cancel opening order
						err = b.tryToCancelOrder(pair, openOrder.OrderID, 100)
						if err != nil {
							hasError = 1
						} else {
							needToBuyMore = 1
							hasError = 0
							// Stoploss for executed amount
							if openOrder.Status == "PARTIALLY_FILLED" {
								// Set take profit and stoploss
								go b.TryToSetTakeProfitAndStopLost(pairInfo, helper.StringToFloat64(openOrder.Price), helper.StringToFloat64(openOrder.ExecutedQuantity), tk, sl, 100, delay, c)
							}
						}

					} else {
						hasError = 1
					}
				}
			} else {
				hasError = 1
			}
		}
		// fmt.Printf("Need to buy more :%v\n", needToBuyMore)
		if done != 1 && hasError != 1 && needToBuyMore == 1 && willBuyAmout >= pairInfo.LotSize.Min {
			newOrder, err := b.tryToBuyBestByMarket(pair, willBuyAmout, buyPrice, maxPrice, race, delay)
			if err == nil {
				fmt.Printf("Order success: %v @ %v\n", newOrder.ExecutedQuantity, newOrder.Price)
				openOderID = newOrder.OrderID
				needToBuyMore = 0
			} else {
				fmt.Printf("Error when set limit order:%v\n", err)
			}
		}
		// try again after 0.2 s
		wait := time.Duration(delay) * time.Millisecond
		// try again after 0.2 s
		time.Sleep(wait)
	}
	return nil
}

func (b *binance) TryToSetTakeProfitAndStopLost(pairInfo Pair, filledPrice float64, amount float64, tk float64, sl float64, maxTry int, delay int, contact chan error) error {
	done := 0
	tried := 0

	profitPrice := filledPrice + b.CalculateChangePrice(filledPrice, tk, pairInfo.PriceFilter.Tick, true)
	lossPrice := filledPrice - b.CalculateChangePrice(filledPrice, sl, pairInfo.PriceFilter.Tick, false)

	fmt.Printf("Monitoring for  %s @ %v with profit @ %v and loss @ %v\n", pairInfo.Symbol, filledPrice, profitPrice, lossPrice)

	for done != 1 && tried <= maxTry {
		fmt.Printf("Try to take profit for %v @ %v (%v USDT)\n", amount, profitPrice, amount*profitPrice)
		profitOrder, err := b.createLimitOrder(binanceLib.SideTypeSell, binanceLib.OrderTypeLimit, pairInfo.Symbol, helper.Float64ToString(profitPrice), helper.Float64ToString(amount))
		if err == nil {
			fmt.Printf("Will take profit with %v\n", profitOrder)

			b.MonitorAndStopLoss(pairInfo.Symbol, profitOrder, lossPrice, delay, contact)
			done = 1
		} else {
			fmt.Printf("Error when setting profit order:%v\n", err)
		}
		tried++
		wait := time.Duration(delay) * time.Millisecond
		// try again after 0.2 s
		time.Sleep(wait)
	}
	return nil
}

func (b *binance) MonitorAndStopLoss(pair string, order *binanceLib.CreateOrderResponse, stopPrice float64, interval int, contact chan error) error {
	fmt.Printf("Motitoring pair: %s\n", pair)
	done := 0

	needToStoploss := 0
	needCancelOldOrder := 1

	for done != 1 {
		hasError := 0
		currentOrder, err := b.GetOrderInfo(pair, order.OrderID, 100)
		if err == nil {
			hasError = 0
			if currentOrder.Status == "FILLED" {
				fmt.Printf("*** Taken profit @ %v", currentOrder.Price)
				done = 1
				needToStoploss = 0
				return nil
			}
		} else {
			hasError = 1

		}
		// Check price
		if needToStoploss == 0 && hasError == 0 && done != 1 {
			bestPrice, err := b.GetPairCurrentBestPrice(pair)
			if err == nil {
				hasError = 0
				if bestPrice.BidPrice <= stopPrice {
					needToStoploss = 1
				}
			} else {
				hasError = 1
			}
		}
		if needToStoploss == 1 && hasError == 0 && done != 1 {
			remainAmout := helper.StringToFloat64(order.OrigQuantity) - helper.StringToFloat64(currentOrder.ExecutedQuantity)
			fmt.Printf("Try to stop loss for %v @ %v (%v USDT)\n", remainAmout, stopPrice, remainAmout*stopPrice)
			// Cancel open order
			if needCancelOldOrder == 1 {
				err := b.tryToCancelOrder(pair, order.OrderID, 100)
				if err == nil {
					needCancelOldOrder = 0
					hasError = 0
				} else {
					hasError = 1
				}
			}
			if hasError == 0 {
				// Try to sell at market price
				_, err := b.worker.NewCreateOrderService().Symbol(pair).
					Side(binanceLib.SideTypeSell).Type(binanceLib.OrderTypeMarket).Quantity(helper.Float64ToString(remainAmout)).Do(context.Background())
				if err == nil {
					done = 1
					// contact <- fmt.Errorf(":(((( Stoploss @ %v", slOrder)
					return nil
				} else {
					fmt.Printf("Error when trying to stoploss: %v\n", err)
				}
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
		wait := 500 * time.Millisecond
		// try again after 0.2 s
		time.Sleep(wait)
		tried++
	}
	return nil, errors.New("Cannot get order status")
}

// Incase race, buyPrice will be ignore
func (b *binance) tryToBuyBestByMarket(pair string, amount float64, buyPrice float64, maxPrice float64, race int, interval int) (*binanceLib.CreateOrderResponse, error) {
	ok := 0
	for ok == 0 {
		if race == 1 {
			pairInfo, err := b.GetPairInfo(pair)
			if err == nil {
				bestPrice, err := b.GetPairCurrentBestPrice(pair)
				fmt.Printf("Current best bid: %v\n", bestPrice.BidPrice)
				if err == nil {
					tryPrice := bestPrice.BidPrice + pairInfo.PriceFilter.Tick
					if tryPrice > maxPrice {
						ok = -1
						fmt.Printf("Price is too high @ %v\n", bestPrice.BidPrice)
						return nil, errors.New("Price too high")
					}
					fmt.Printf("Setting order: %v\n", tryPrice)
					fmt.Printf("Setting order @  : %v (after converted)\n", helper.Float64ToString(tryPrice))
					order, err := b.createLimitOrder(binanceLib.SideTypeBuy, binanceLib.OrderTypeLimit, pair, helper.Float64ToString(tryPrice), helper.Float64ToString(amount))
					if err == nil {
						ok = 1
						return order, err
					} else {
						fmt.Printf("Setting order error: %v\n", err)
					}
				}
			}
		} else {
			order, err := b.createLimitOrder(binanceLib.SideTypeBuy, binanceLib.OrderTypeLimit, pair, helper.Float64ToString(buyPrice), helper.Float64ToString(amount))
			if err == nil {
				ok = 1
				return order, err
			} else {
				fmt.Printf("Error when trying to buy: %v\n", err)
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
		wait := 500 * time.Millisecond
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

func (b *binance) CalculateChangePrice(originalPrice float64, percent float64, tickSize float64, increase bool) float64 {
	idealChange := originalPrice
	if increase == true {
		idealChange = originalPrice * (1 + percent)
	} else {
		idealChange = originalPrice * (1 - percent)
	}
	delta := math.Abs(idealChange - originalPrice)
	return delta - math.Mod(delta, tickSize)

}

func (b *binance) tryToGetListOpenOrders(pair string, try int) ([]*binanceLib.Order, error) {

	var empty []*binanceLib.Order

	ok := 0
	tried := 0
	for ok == 0 && tried < try {
		results, err := b.worker.NewListOpenOrdersService().Symbol(pair).Do(context.Background())
		if err == nil {
			ok = 1
			return results, err
		}
		// try again after 0.2 s
		wait := 500 * time.Millisecond
		time.Sleep(wait)
		tried++
	}
	return empty, fmt.Errorf("Cannot get list opening orders for pair %s", pair)
}

func getBestPriceFromDepthEvent(event *binanceLib.WsPartialDepthEvent) (float64, error) {
	bestPrice := 0.0
	if len(event.Bids) == 0 {
		return bestPrice, errors.New("Empty data from server")
	}
	bestPrice = helper.StringToFloat64(event.Bids[0].Price)
	return bestPrice, nil
}

func (b *binance) TryToStopLossForOpenOders(pair string, sl float64, delay int, terminater chan error) []error {
	var results []error
	fc := make(chan string)
	// done := 0
	doneSL := 0
	age := 0

	wsDepthHandler := func(event *binanceLib.WsPartialDepthEvent) {
		age++
		bestPirce, err := getBestPriceFromDepthEvent(event)
		if err == nil {
			if age % 30 == 0 {
				fmt.Printf("Best Price : %v, Stoploss: %v\n", bestPirce, sl)
			}
			if bestPirce <= sl {
				var errs []error
				for doneSL == 0 {
					fmt.Println("Trying to stoploss\n")
					orders, err := b.tryToGetListOpenOrders(pair, 10)
					if err == nil {
						for _, order := range orders {
							err = b.tryToCancelOrder(pair, order.OrderID, 10)
							if err == nil {
								remainAmout := helper.StringToFloat64(order.OrigQuantity) - helper.StringToFloat64(order.ExecutedQuantity)
								err = b.tryToSellAnyway(pair, remainAmout)
								if err != nil {
									fmt.Println(err)
									errs = append(errs, err)
								}
							} else {
								errs = append(errs, err)
							}
						}
					} else {
						fmt.Println(err)
						errs = append(errs, err)
					}

					if len(errs) == 0 {
						doneSL = 1
						fc <- "**** Done stop loss"
					} else {
						wait := 1000 * time.Millisecond
						time.Sleep(wait)
					}
					
				}
			}
		
		} else {
			fmt.Println(err)
		}
	
	}
	errHandler := func(err error) {
		results = append(results, err)
		fmt.Println(err)
		terminater <- fmt.Errorf("%s", err)
	}
	_, stopC, err := binanceLib.WsPartialDepthServe(pair, "5", wsDepthHandler, errHandler)
	if err != nil {
		fmt.Println(err)
		results = append(results, err)
		stopC <- struct{}{}
		terminater <- fmt.Errorf("%s", err)
	}
	fmt.Printf("Stop monitoring: %v\n", <-fc)
	return results
}

func (b *binance) tryToSellAnyway(pair string, amount float64) error {
	ok := 0
	tried := 0
	for ok == 0 && tried < 10 {
		order, err := b.worker.NewCreateOrderService().Symbol(pair).
			Side(binanceLib.SideTypeSell).Type(binanceLib.OrderTypeMarket).Quantity(helper.Float64ToString(amount)).Do(context.Background())
		if err == nil {
			ok = 1
			fmt.Printf("Sold %s by order ID: %v", pair, order.OrderID)
			return nil
		}
		// try again after 0.2 s
		wait := 500 * time.Millisecond
		time.Sleep(wait)
		tried++
	}
	return fmt.Errorf("Cannot sell pair %s anyway", pair)
}
