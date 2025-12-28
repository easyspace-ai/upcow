
import re
import pandas as pd
import numpy as np

log_file = '/workspace/lab/Backtest_12_27.log'

data = []
with open(log_file, 'r') as f:
    current_ts = None
    fut = None
    poly = None
    fair_up = None
    
    for line in f:
        # Extract timestamp
        ts_match = re.search(r'\[(\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}\.\d+Z)\]', line)
        if ts_match:
            current_ts = ts_match.group(1)
            continue
            
        # Extract data line
        if 'fut:' in line and 'poly:' in line:
            # fut:88875.05 poly:88885.89...
            parts = line.split()
            fut_val = float(parts[0].split(':')[1])
            poly_val = float(parts[1].split(':')[1])
            fut = fut_val
            poly = poly_val
            
        if 'FairUp:' in line:
            # FairUp:0.4971 FairDown:0.5029
            parts = line.split()
            fair_val = float(parts[0].split(':')[1])
            
            if current_ts and fut is not None:
                data.append({
                    'ts': current_ts,
                    'fut': fut,
                    'poly': poly,
                    'fair_up': fair_val
                })

df = pd.DataFrame(data)
df['ts'] = pd.to_datetime(df['ts'])
df.set_index('ts', inplace=True)

# Calculate Deltas
df['delta_fut'] = df['fut'].diff()
df['delta_fair'] = df['fair_up'].diff()

# Calculate "Binary Delta" (Change in Fair Value / Change in Underlying)
# Filter out zero changes to avoid inf
mask = df['delta_fut'] != 0
df_filtered = df[mask].copy()
df_filtered['sensitivity'] = df_filtered['delta_fair'] / df_filtered['delta_fut']

print(df_filtered[['fut', 'fair_up', 'sensitivity']].describe())
print("\nSample Data:")
print(df_filtered[['fut', 'fair_up', 'sensitivity']].head(10))
