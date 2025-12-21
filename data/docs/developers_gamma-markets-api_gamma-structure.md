# Gamma Structure - Polymarket Documentation

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
Gamma Structure
Gamma Structure
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
On this page
Detail
Example
Gamma Structure
Gamma Structure
Gamma provides some organizational models. These include events, and markets. The most fundamental element is always markets and the other models simply provide additional organization.


​
Detail






Market




Contains data related to a market that is traded on. Maps onto a pair of clob token ids, a market address, a question id and a condition id








Event




Contains a set of markets


Variants:




Event with 1 market (i.e., resulting in an SMP)


Event with 2 or more markets (i.e., resulting in an GMP)












​
Example




[Event]
 Where will Barron Trump attend College?




[Market]
 Will Barron attend Georgetown?


[Market]
 Will Barron attend NYU?


[Market]
 Will Barron attend UPenn?


[Market]
 Will Barron attend Harvard?


[Market]
 Will Barron attend another college?






Overview
Fetching Markets
⌘
I
github
Powered by Mintlify