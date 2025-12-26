package types

const (
	// Server Time
	TIME = "/time"
	// API Key endpoints
	CREATE_API_KEY = "/auth/api-key"

	GET_API_KEYS = "/auth/api-keys"

	DELETE_API_KEY = "/auth/api-key"

	DERIVE_API_KEY = "/auth/derive-api-key"

	CLOSED_ONLY = "/auth/ban-status/closed-only"

	// Builder API Key endpoints
	CREATE_BUILDER_API_KEY = "/auth/builder-api-key"

	GET_BUILDER_API_KEYS = "/auth/builder-api-key"

	REVOKE_BUILDER_API_KEY = "/auth/builder-api-key"

	// Markets
	GET_SAMPLING_SIMPLIFIED_MARKETS = "/sampling-simplified-markets"

	GET_SAMPLING_MARKETS = "/sampling-markets"

	GET_SIMPLIFIED_MARKETS = "/simplified-markets"

	GET_MARKETS = "/markets"

	GET_MARKET = "/markets/"

	GET_ORDER_BOOK = "/book"

	GET_ORDER_BOOKS = "/books"

	GET_MIDPOINT = "/midpoint"

	GET_MIDPOINTS = "/midpoints"

	GET_PRICE = "/price"

	GET_PRICES = "/prices"

	GET_SPREAD = "/spread"

	GET_SPREADS = "/spreads"

	GET_LAST_TRADE_PRICE = "/last-trade-price"

	GET_LAST_TRADES_PRICES = "/last-trades-prices"

	GET_TICK_SIZE = "/tick-size"

	GET_NEG_RISK = "/neg-risk"

	GET_FEE_RATE = "/fee-rate"

	// Order endpoints
	POST_ORDER = "/order"

	POST_ORDERS = "/orders"

	CANCEL_ORDER = "/order"

	CANCEL_ORDERS = "/orders"

	GET_ORDER = "/data/order/"

	CANCEL_ALL = "/cancel-all"

	CANCEL_MARKET_ORDERS = "/cancel-market-orders"

	GET_OPEN_ORDERS = "/data/orders"

	GET_TRADES = "/dataapi/trades"

	IS_ORDER_SCORING = "/order-scoring"

	ARE_ORDERS_SCORING = "/orders-scoring"

	// Price history
	GET_PRICES_HISTORY = "/prices-history"

	// Notifications
	GET_NOTIFICATIONS = "/notifications"

	DROP_NOTIFICATIONS = "/notifications"

	// Balance
	GET_BALANCE_ALLOWANCE = "/balance-allowance"

	UPDATE_BALANCE_ALLOWANCE = "/balance-allowance/update"

	// Live activity
	GET_MARKET_TRADES_EVENTS = "/live-activity/events/"

	// Rewards
	GET_EARNINGS_FOR_USER_FOR_DAY = "/rewards/user"

	GET_TOTAL_EARNINGS_FOR_USER_FOR_DAY = "/rewards/user/total"

	GET_LIQUIDITY_REWARD_PERCENTAGES = "/rewards/user/percentages"

	GET_REWARDS_MARKETS_CURRENT = "/rewards/markets/current"

	GET_REWARDS_MARKETS = "/rewards/markets/"

	GET_REWARDS_EARNINGS_PERCENTAGES = "/rewards/user/markets"

	// Builder endpoints
	GET_BUILDER_TRADES = "/builder/trades"
)
