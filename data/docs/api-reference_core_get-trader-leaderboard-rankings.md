# Get trader leaderboard rankings - Polymarket Documentation

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
Get trader leaderboard rankings
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
Get trader leaderboard rankings
cURL
Copy
Ask AI
curl
 --request
 GET
 \


  --url
 https://data-api.polymarket.com/v1/leaderboard
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


    "proxyWallet"
: 
"0x56687bf447db6ffa42ffe2204a05edaa20f55839"
,


    "userName"
: 
"<string>"
,


    "vol"
: 
123
,


    "pnl"
: 
123
,


    "profileImage"
: 
"<string>"
,


    "xUsername"
: 
"<string>"
,


    "verifiedBadge"
: 
true


  }


]
Core
Get trader leaderboard rankings
Returns trader leaderboard rankings filtered by category, time period, and ordering.
GET
/
v1
/
leaderboard
Try it
Get trader leaderboard rankings
cURL
Copy
Ask AI
curl
 --request
 GET
 \


  --url
 https://data-api.polymarket.com/v1/leaderboard
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


    "proxyWallet"
: 
"0x56687bf447db6ffa42ffe2204a05edaa20f55839"
,


    "userName"
: 
"<string>"
,


    "vol"
: 
123
,


    "pnl"
: 
123
,


    "profileImage"
: 
"<string>"
,


    "xUsername"
: 
"<string>"
,


    "verifiedBadge"
: 
true


  }


]
Query Parameters
​
category
enum<string>
default:
OVERALL
Market category for the leaderboard
Available options
:
 
OVERALL
,
 
POLITICS
,
 
SPORTS
,
 
CRYPTO
,
 
CULTURE
,
 
MENTIONS
,
 
WEATHER
,
 
ECONOMICS
,
 
TECH
,
 
FINANCE
 
​
timePeriod
enum<string>
default:
DAY
Time period for leaderboard results
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
orderBy
enum<string>
default:
PNL
Leaderboard ordering criteria
Available options
:
 
PNL
,
 
VOL
 
​
limit
integer
default:
25
Max number of leaderboard traders to return
Required range
: 
1 <= x <= 50
​
offset
integer
default:
0
Starting index for pagination
Required range
: 
0 <= x <= 1000
​
user
string
Limit leaderboard to a single user by address
User Profile Address (0x-prefixed, 40 hex chars)
Example
:
"0x56687bf447db6ffa42ffe2204a05edaa20f55839"
​
userName
string
Limit leaderboard to a single username
Response
200
application/json
Success
​
rank
string
The rank position of the trader
​
proxyWallet
string
User Profile Address (0x-prefixed, 40 hex chars)
Example
:
"0x56687bf447db6ffa42ffe2204a05edaa20f55839"
​
userName
string
The trader's username
​
vol
number
Trading volume for this trader
​
pnl
number
Profit and loss for this trader
​
profileImage
string
URL to the trader's profile image
​
xUsername
string
The trader's X (Twitter) username
​
verifiedBadge
boolean
Whether the trader has a verified badge
Get closed positions for a user
Get total markets a user has traded
⌘
I
github
Powered by Mintlify