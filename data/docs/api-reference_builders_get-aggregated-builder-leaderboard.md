# Get aggregated builder leaderboard - Polymarket Documentation

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
Get aggregated builder leaderboard
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
Get aggregated builder leaderboard
cURL
Copy
Ask AI
curl
 --request
 GET
 \


  --url
 https://data-api.polymarket.com/v1/builders/leaderboard
200
400
500
Copy
Ask AI
[


  {


    "rank"
: 
"<string>"
,


    "builder"
: 
"<string>"
,


    "volume"
: 
123
,


    "activeUsers"
: 
123
,


    "verified"
: 
true
,


    "builderLogo"
: 
"<string>"


  }


]
Builders
Get aggregated builder leaderboard
Returns aggregated builder rankings with one entry per builder showing total for the specified time period. Supports pagination.
GET
/
v1
/
builders
/
leaderboard
Try it
Get aggregated builder leaderboard
cURL
Copy
Ask AI
curl
 --request
 GET
 \


  --url
 https://data-api.polymarket.com/v1/builders/leaderboard
200
400
500
Copy
Ask AI
[


  {


    "rank"
: 
"<string>"
,


    "builder"
: 
"<string>"
,


    "volume"
: 
123
,


    "activeUsers"
: 
123
,


    "verified"
: 
true
,


    "builderLogo"
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
The time period to aggregate results over.
Available options
:
 
DAY
,
 
WEEK
,
 
MONTH
,
 
ALL
 
​
limit
integer
default:
25
Maximum number of builders to return
Required range
: 
0 <= x <= 50
​
offset
integer
default:
0
Starting index for pagination
Required range
: 
0 <= x <= 1000
Response
200
application/json
Success
​
rank
string
The rank position of the builder
​
builder
string
The builder name or identifier
​
volume
number
Total trading volume attributed to this builder
​
activeUsers
integer
Number of active users for this builder
​
verified
boolean
Whether the builder is verified
​
builderLogo
string
URL to the builder's logo image
Get live volume for an event
Get daily builder volume time-series
⌘
I
github
Powered by Mintlify