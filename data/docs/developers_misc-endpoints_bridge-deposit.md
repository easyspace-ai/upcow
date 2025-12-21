# Create Deposit - Polymarket Documentation

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
Bridge & Swap
Create Deposit
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
Create deposit addresses
cURL
Copy
Ask AI
curl
 --request
 POST
 \


  --url
 https://bridge.polymarket.com/deposit
 \


  --header
 'Content-Type: application/json'
 \


  --data
 '


{


  "address": "0x56687bf447db6ffa42ffe2204a05edaa20f55839"


}


'
201
400
500
Copy
Ask AI
{


  "address"
: 
"0x56687bf447db6ffa42ffe2204a05edaa20f55839"
,


  "depositAddresses"
: [


    {


      "chainId"
: 
"1"
,


      "chainName"
: 
"Ethereum"
,


      "tokenAddress"
: 
"0xA0b86991c6218b36c1d19D4a2e9Eb0cE3606eB48"
,


      "tokenSymbol"
: 
"USDC"
,


      "depositAddress"
: 
"0x1234567890abcdef1234567890abcdef12345678"


    }


  ]


}
Bridge & Swap
Create Deposit
Generate unique deposit addresses for bridging assets to Polymarket.


How it works:




Request deposit addresses for your Polymarket wallet


Receive unique deposit addresses for each supported chain/token


Send assets to the appropriate deposit address


Assets are automatically bridged and swapped to USDC.e on Polygon


USDC.e is credited to your Polymarket wallet for trading


POST
/
deposit
Try it
Create deposit addresses
cURL
Copy
Ask AI
curl
 --request
 POST
 \


  --url
 https://bridge.polymarket.com/deposit
 \


  --header
 'Content-Type: application/json'
 \


  --data
 '


{


  "address": "0x56687bf447db6ffa42ffe2204a05edaa20f55839"


}


'
201
400
500
Copy
Ask AI
{


  "address"
: 
"0x56687bf447db6ffa42ffe2204a05edaa20f55839"
,


  "depositAddresses"
: [


    {


      "chainId"
: 
"1"
,


      "chainName"
: 
"Ethereum"
,


      "tokenAddress"
: 
"0xA0b86991c6218b36c1d19D4a2e9Eb0cE3606eB48"
,


      "tokenSymbol"
: 
"USDC"
,


      "depositAddress"
: 
"0x1234567890abcdef1234567890abcdef12345678"


    }


  ]


}
​
Create Deposit


Generate unique deposit addresses for bridging assets to Polymarket.


​
How It Works




Call this endpoint with your Polymarket wallet address


Receive unique deposit addresses for each supported chain/token


Send assets to the appropriate deposit address


Assets are automatically bridged to Polygon and swapped to USDC.e


USDC.e is credited to your Polymarket wallet for trading






Note:
 All deposits are automatically converted to USDC.e on Polygon, which is the collateral used for all Polymarket trades.






⚠️ 
Minimum Deposit Amounts:
 Each chain and token has a minimum deposit amount required. Please check the 
Get Supported Assets
 endpoint to see the minimum amounts before sending funds.


Body
application/json
​
address
string
required
Your Polymarket wallet address
Example
:
"0x56687bf447db6ffa42ffe2204a05edaa20f55839"
Response
201
application/json
Deposit addresses created successfully
​
address
string
Your Polymarket wallet address
Example
:
"0x56687bf447db6ffa42ffe2204a05edaa20f55839"
​
depositAddresses
object[]
List of deposit addresses for different chains and tokens
Show
 
child attributes
​
depositAddresses.
chainId
string
The chain ID for the deposit
Example
:
"1"
​
depositAddresses.
chainName
string
Human-readable chain name
Example
:
"Ethereum"
​
depositAddresses.
tokenAddress
string
Token contract address on the source chain
Example
:
"0xA0b86991c6218b36c1d19D4a2e9Eb0cE3606eB48"
​
depositAddresses.
tokenSymbol
string
Token symbol
Example
:
"USDC"
​
depositAddresses.
depositAddress
string
The unique deposit address for this chain/token combination
Example
:
"0x1234567890abcdef1234567890abcdef12345678"
Overview
Get Supported Assets
⌘
I
github
Powered by Mintlify