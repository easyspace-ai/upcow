# Get daily builder volume time-series - Polymarket Documentation

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
Builders
Get daily builder volume time-series
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
Misc
Builders
GET
Get aggregated builder leaderboard
GET
Get daily builder volume time-series
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
Get daily builder volume time-series
cURL
Copy
Ask AI
curl
 --request
 GET
 \


  --url
 https://data-api.polymarket.com/v1/builders/volume
200
400
500
Copy
Ask AI
[


  {


    "dt"
: 
"2025-11-15T00:00:00Z"
,


    "builder"
: 
"<string>"
,


    "builderLogo"
: 
"<string>"
,


    "verified"
: 
true
,


    "volume"
: 
123
,


    "activeUsers"
: 
123
,


    "rank"
: 
"<string>"


  }


]
Builders
Get daily builder volume time-series
Returns daily time-series volume data with multiple entries per builder (one per day), each including a 
dt
 timestamp. No pagination.
GET
/
v1
/
builders
/
volume
Try it
Get daily builder volume time-series
cURL
Copy
Ask AI
curl
 --request
 GET
 \


  --url
 https://data-api.polymarket.com/v1/builders/volume
200
400
500
Copy
Ask AI
[


  {


    "dt"
: 
"2025-11-15T00:00:00Z"
,


    "builder"
: 
"<string>"
,


    "builderLogo"
: 
"<string>"
,


    "verified"
: 
true
,


    "volume"
: 
123
,


    "activeUsers"
: 
123
,


    "rank"
: 
"<string>"


  }


]
Query Parameters
​
timePeriod
enum<string>
default:
DAY
The time period to fetch daily records for.
Available options
:
 
DAY
,
 
WEEK
,
 
MONTH
,
 
ALL
 
Response
200
application/json
Success - Returns array of daily volume records
​
dt
string<date-time>
The timestamp for this volume entry in ISO 8601 format
Example
:
"2025-11-15T00:00:00Z"
​
builder
string
The builder name or identifier
​
builderLogo
string
URL to the builder's logo image
​
verified
boolean
Whether the builder is verified
​
volume
number
Trading volume for this builder on this date
​
activeUsers
integer
Number of active users for this builder on this date
​
rank
string
The rank position of the builder on this date
Get aggregated builder leaderboard
Overview
⌘
I
github
Powered by Mintlify