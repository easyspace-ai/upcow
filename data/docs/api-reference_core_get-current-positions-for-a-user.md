# Get current positions for a user - Polymarket Documentation

Skip to main content
Polymarket Documentation
 home page
Search...
⌘
K
Main Site
Main Site
Search...
Navigation
Core
Get current positions for a user
User Guide
For Developers
Changelog
Polymarket
Discord Community
Twitter
Developer Quickstart
Developer Quickstart
Your First Order
Glossary
API Rate Limits
Endpoints
Polymarket Builders Program
Builder Program Introduction
Builder Profile & Keys
Order Attribution
Relayer Client
Examples
Central Limit Order Book
CLOB Introduction
Status
Quickstart
Authentication
Client
REST API
Historical Timeseries Data
Order Management
Trades
Websocket
WSS Overview
WSS Quickstart
WSS Authentication
User Channel
Market Channel
Real Time Data Stream
RTDS Overview
RTDS Crypto Prices
RTDS Comments
Gamma Structure
Overview
Gamma Structure
Fetching Markets
Gamma Endpoints
Health
Sports
Tags
Events
Markets
Series
Comments
Search
Data-API
Health
Core
GET
Get current positions for a user
GET
Get trades for a user or markets
GET
Get user activity
GET
Get top holders for markets
GET
Get total value of a user's positions
GET
Get closed positions for a user
GET
Get trader leaderboard rankings
Misc
Builders
Bridge & Swap
Overview
POST
Create Deposit
GET
Get Supported Assets
Subgraph
Overview
Resolution
Resolution
Rewards
Liquidity Rewards
Conditional Token Frameworks
Overview
Splitting USDC
Merging Tokens
Reedeeming Tokens
Deployment and Additional Information
Proxy Wallets
Proxy wallet
Negative Risk
Overview
Get current positions for a user
cURL
Copy
Ask AI
curl
 --request
 GET
 \


  --url
 https://data-api.polymarket.com/positions
200
400
401
500
Copy
Ask AI
[


  {


    "proxyWallet"
: 
"0x56687bf447db6ffa42ffe2204a05edaa20f55839"
,


    "asset"
: 
"<string>"
,


    "conditionId"
: 
"0xdd22472e552920b8438158ea7238bfadfa4f736aa4cee91a6b86c39ead110917"
,


    "size"
: 
123
,


    "avgPrice"
: 
123
,


    "initialValue"
: 
123
,


    "currentValue"
: 
123
,


    "cashPnl"
: 
123
,


    "percentPnl"
: 
123
,


    "totalBought"
: 
123
,


    "realizedPnl"
: 
123
,


    "percentRealizedPnl"
: 
123
,


    "curPrice"
: 
123
,


    "redeemable"
: 
true
,


    "mergeable"
: 
true
,


    "title"
: 
"<string>"
,


    "slug"
: 
"<string>"
,


    "icon"
: 
"<string>"
,


    "eventSlug"
: 
"<string>"
,


    "outcome"
: 
"<string>"
,


    "outcomeIndex"
: 
123
,


    "oppositeOutcome"
: 
"<string>"
,


    "oppositeAsset"
: 
"<string>"
,


    "endDate"
: 
"<string>"
,


    "negativeRisk"
: 
true


  }


]
Core
Get current positions for a user
Returns positions filtered by user and optional filters.
GET
/
positions
Try it
Get current positions for a user
cURL
Copy
Ask AI
curl
 --request
 GET
 \


  --url
 https://data-api.polymarket.com/positions
200
400
401
500
Copy
Ask AI
[


  {


    "proxyWallet"
: 
"0x56687bf447db6ffa42ffe2204a05edaa20f55839"
,


    "asset"
: 
"<string>"
,


    "conditionId"
: 
"0xdd22472e552920b8438158ea7238bfadfa4f736aa4cee91a6b86c39ead110917"
,


    "size"
: 
123
,


    "avgPrice"
: 
123
,


    "initialValue"
: 
123
,


    "currentValue"
: 
123
,


    "cashPnl"
: 
123
,


    "percentPnl"
: 
123
,


    "totalBought"
: 
123
,


    "realizedPnl"
: 
123
,


    "percentRealizedPnl"
: 
123
,


    "curPrice"
: 
123
,


    "redeemable"
: 
true
,


    "mergeable"
: 
true
,


    "title"
: 
"<string>"
,


    "slug"
: 
"<string>"
,


    "icon"
: 
"<string>"
,


    "eventSlug"
: 
"<string>"
,


    "outcome"
: 
"<string>"
,


    "outcomeIndex"
: 
123
,


    "oppositeOutcome"
: 
"<string>"
,


    "oppositeAsset"
: 
"<string>"
,


    "endDate"
: 
"<string>"
,


    "negativeRisk"
: 
true


  }


]
Query Parameters
​
user
string
required
User address (required)
User Profile Address (0x-prefixed, 40 hex chars)
Example
:
"0x56687bf447db6ffa42ffe2204a05edaa20f55839"
​
market
string[]
Comma-separated list of condition IDs. Mutually exclusive with eventId.
0x-prefixed 64-hex string
​
eventId
integer[]
Comma-separated list of event IDs. Mutually exclusive with market.
Required range
: 
x >= 1
​
sizeThreshold
number
default:
1
Required range
: 
x >= 0
​
redeemable
boolean
default:
false
​
mergeable
boolean
default:
false
​
limit
integer
default:
100
Required range
: 
0 <= x <= 500
​
offset
integer
default:
0
Required range
: 
0 <= x <= 10000
​
sortBy
enum<string>
default:
TOKENS
Available options
:
 
CURRENT
,
 
INITIAL
,
 
TOKENS
,
 
CASHPNL
,
 
PERCENTPNL
,
 
TITLE
,
 
RESOLVING
,
 
PRICE
,
 
AVGPRICE
 
​
sortDirection
enum<string>
default:
DESC
Available options
:
 
ASC
,
 
DESC
 
​
title
string
Maximum string length: 
100
Response
200
application/json
Success
​
proxyWallet
string
User Profile Address (0x-prefixed, 40 hex chars)
Example
:
"0x56687bf447db6ffa42ffe2204a05edaa20f55839"
​
asset
string
​
conditionId
string
0x-prefixed 64-hex string
Example
:
"0xdd22472e552920b8438158ea7238bfadfa4f736aa4cee91a6b86c39ead110917"
​
size
number
​
avgPrice
number
​
initialValue
number
​
currentValue
number
​
cashPnl
number
​
percentPnl
number
​
totalBought
number
​
realizedPnl
number
​
percentRealizedPnl
number
​
curPrice
number
​
redeemable
boolean
​
mergeable
boolean
​
title
string
​
slug
string
​
icon
string
​
eventSlug
string
​
outcome
string
​
outcomeIndex
integer
​
oppositeOutcome
string
​
oppositeAsset
string
​
endDate
string
​
negativeRisk
boolean
Health check
Get trades for a user or markets
⌘
I
github
Powered by Mintlify