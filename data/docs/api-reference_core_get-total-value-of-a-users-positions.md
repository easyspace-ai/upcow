# Get total value of a user's positions - Polymarket Documentation

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
Get total value of a user's positions
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
Get total value of a user's positions
cURL
Copy
Ask AI
curl
 --request
 GET
 \


  --url
 https://data-api.polymarket.com/value
200
400
500
Copy
Ask AI
[


  {


    "user"
: 
"0x56687bf447db6ffa42ffe2204a05edaa20f55839"
,


    "value"
: 
123


  }


]
Core
Get total value of a user's positions
GET
/
value
Try it
Get total value of a user's positions
cURL
Copy
Ask AI
curl
 --request
 GET
 \


  --url
 https://data-api.polymarket.com/value
200
400
500
Copy
Ask AI
[


  {


    "user"
: 
"0x56687bf447db6ffa42ffe2204a05edaa20f55839"
,


    "value"
: 
123


  }


]
Query Parameters
​
user
string
required
User Profile Address (0x-prefixed, 40 hex chars)
Example
:
"0x56687bf447db6ffa42ffe2204a05edaa20f55839"
​
market
string[]
0x-prefixed 64-hex string
Response
200
application/json
Success
​
user
string
User Profile Address (0x-prefixed, 40 hex chars)
Example
:
"0x56687bf447db6ffa42ffe2204a05edaa20f55839"
​
value
number
Get top holders for markets
Get closed positions for a user
⌘
I
github
Powered by Mintlify