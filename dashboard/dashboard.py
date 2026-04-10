"""
Inferno control loop dashboard.

Reads a JSONL cycle log and displays five panels:
  1. Workload   — arrival rate & throughput (RPM) per server
  2. Performance — attained ITL & TTFT vs SLO targets per server
  3. Controls   — replicas per server (left y) + total cost (right y)
  4. Capacity   — accelerators allocated vs available per type
  5. Internals  — EKF alpha / beta / gamma per model/accelerator

Usage:
    pip install -r requirements.txt
    INFERNO_CYCLE_LOG=../inferno-cycles.jsonl python dashboard.py

Environment variables:
    INFERNO_CYCLE_LOG     path to the JSONL file  (default: inferno-cycles.jsonl)
    INFERNO_DASH_REFRESH  auto-refresh in ms       (default: 5000)
    INFERNO_DASH_PORT     dashboard port           (default: 8050)
"""

import json
import os

import pandas as pd
import plotly.graph_objects as go
from dash import Dash, Input, Output, dcc, html
from plotly.subplots import make_subplots

LOG_PATH = os.environ.get("INFERNO_CYCLE_LOG", "inferno-cycles.jsonl")
REFRESH_MS = int(os.environ.get("INFERNO_DASH_REFRESH", "5000"))
PORT = int(os.environ.get("INFERNO_DASH_PORT", "8050"))

# ---------------------------------------------------------------------------
# Data loading
# ---------------------------------------------------------------------------


def load_data():
    """Return (servers_df, internals_df, capacity_df) parsed from the JSONL log.

    servers_df columns:  cycle, ts, name, class, model,
                         rpm, throughput, avgInTok, avgOutTok,
                         itl, ttft, sloItl, sloTtft,
                         accelerator, replicas, cost
    internals_df columns: cycle, ts, model, acc, alpha, beta, gamma
    capacity_df columns:  cycle, ts, type, allocated, available
    """
    records = []
    try:
        with open(LOG_PATH) as f:
            for line in f:
                line = line.strip()
                if line:
                    try:
                        records.append(json.loads(line))
                    except json.JSONDecodeError:
                        pass
    except FileNotFoundError:
        pass

    if not records:
        return pd.DataFrame(), pd.DataFrame(), pd.DataFrame()

    servers_df = pd.json_normalize(
        records,
        record_path="servers",
        meta=["cycle", "ts", "totalCost"],
        errors="ignore",
    )

    internals_df = pd.json_normalize(
        records,
        record_path="internals",
        meta=["cycle", "ts"],
        errors="ignore",
    )

    # Filter to records that have capacity before normalizing
    records_with_capacity = [r for r in records if "capacity" in r and r["capacity"]]
    if records_with_capacity:
        capacity_df = pd.json_normalize(
            records_with_capacity,
            record_path="capacity",
            meta=["cycle", "ts"],
            errors="ignore",
        )
    else:
        capacity_df = pd.DataFrame()

    return servers_df, internals_df, capacity_df


# ---------------------------------------------------------------------------
# Figure builders
# ---------------------------------------------------------------------------

def _empty(title):
    fig = go.Figure()
    fig.update_layout(title=title, template="plotly_dark",
                      paper_bgcolor="#1e1e1e", plot_bgcolor="#1e1e1e")
    return fig


def fig_workload(df):
    title = "Workload: Traffic Rates & Token Counts"
    if df.empty:
        return _empty(title)

    colors = {}
    palette = [
        "#636efa", "#ef553b", "#00cc96", "#ab63fa",
        "#ffa15a", "#19d3f3", "#ff6692", "#b6e880",
    ]
    for i, server in enumerate(df["name"].unique()):
        colors[server] = palette[i % len(palette)]

    fig = make_subplots(
        rows=2, cols=1, shared_xaxes=True,
        subplot_titles=("Arrival Rate & Throughput (RPM)", "Avg Token Counts (in/out)"),
        vertical_spacing=0.12,
    )

    for server in df["name"].unique():
        s = df[df["name"] == server].sort_values("cycle")
        color = colors[server]

        fig.add_trace(go.Scatter(
            x=s["cycle"], y=s["rpm"],
            mode="lines+markers", name=f"{server} arrival",
            line=dict(color=color), legendgroup=server,
        ), row=1, col=1)
        fig.add_trace(go.Scatter(
            x=s["cycle"], y=s["throughput"],
            mode="lines", name=f"{server} throughput",
            line=dict(color=color, dash="dot"),
            legendgroup=server, showlegend=False,
        ), row=1, col=1)

        fig.add_trace(go.Scatter(
            x=s["cycle"], y=s["avgInTok"],
            mode="lines+markers", name=f"{server} in-tokens",
            line=dict(color=color), legendgroup=server, showlegend=False,
        ), row=2, col=1)
        fig.add_trace(go.Scatter(
            x=s["cycle"], y=s["avgOutTok"],
            mode="lines", name=f"{server} out-tokens",
            line=dict(color=color, dash="dot"),
            legendgroup=server, showlegend=False,
        ), row=2, col=1)

    fig.update_layout(
        title=title, xaxis2_title="Cycle",
        template="plotly_dark", paper_bgcolor="#1e1e1e", plot_bgcolor="#1e1e1e",
        legend=dict(orientation="h", yanchor="bottom", y=1.02),
    )
    fig.update_yaxes(title_text="RPM", row=1, col=1)
    fig.update_yaxes(title_text="Tokens", row=2, col=1)
    return fig


def fig_performance(df):
    title = "Performance: ITL & TTFT vs SLO Targets (ms)"
    if df.empty:
        return _empty(title)

    fig = make_subplots(
        rows=2, cols=1, shared_xaxes=True,
        subplot_titles=("Inter-Token Latency (ITL)", "Time To First Token (TTFT)"),
        vertical_spacing=0.12,
    )

    colors = {}
    palette = [
        "#636efa", "#ef553b", "#00cc96", "#ab63fa",
        "#ffa15a", "#19d3f3", "#ff6692", "#b6e880",
    ]
    for i, server in enumerate(df["name"].unique()):
        colors[server] = palette[i % len(palette)]

    for server in df["name"].unique():
        s = df[df["name"] == server].sort_values("cycle")
        color = colors[server]

        # ITL attained
        fig.add_trace(go.Scatter(
            x=s["cycle"], y=s["itl"],
            mode="lines+markers", name=f"{server}",
            line=dict(color=color), legendgroup=server,
        ), row=1, col=1)
        # ITL SLO target
        fig.add_trace(go.Scatter(
            x=s["cycle"], y=s["sloItl"],
            mode="lines", name=f"{server} SLO",
            line=dict(color=color, dash="dash"),
            legendgroup=server, showlegend=False,
        ), row=1, col=1)

        # TTFT attained
        fig.add_trace(go.Scatter(
            x=s["cycle"], y=s["ttft"],
            mode="lines+markers", name=f"{server}",
            line=dict(color=color), legendgroup=server, showlegend=False,
        ), row=2, col=1)
        # TTFT SLO target
        fig.add_trace(go.Scatter(
            x=s["cycle"], y=s["sloTtft"],
            mode="lines", name=f"{server} SLO",
            line=dict(color=color, dash="dash"),
            legendgroup=server, showlegend=False,
        ), row=2, col=1)

    fig.update_layout(
        title=title, xaxis2_title="Cycle",
        template="plotly_dark", paper_bgcolor="#1e1e1e", plot_bgcolor="#1e1e1e",
        legend=dict(orientation="h", yanchor="bottom", y=1.02),
    )
    fig.update_yaxes(title_text="ms", type="log", row=1, col=1)
    fig.update_yaxes(title_text="ms", type="log", row=2, col=1)
    return fig


def fig_controls(df):
    title = "Controls: Replicas & Total Cost"
    if df.empty:
        return _empty(title)

    fig = make_subplots(
        rows=2, cols=1, shared_xaxes=True,
        subplot_titles=("Replicas per Server", "Total Cost"),
        vertical_spacing=0.12,
    )

    for server in df["name"].unique():
        s = df[df["name"] == server].sort_values("cycle")
        fig.add_trace(go.Scatter(
            x=s["cycle"], y=s["replicas"],
            mode="lines+markers", name=f"{server}",
        ), row=1, col=1)

    cost_per_cycle = (
        df[["cycle", "totalCost"]].dropna().drop_duplicates("cycle").sort_values("cycle")
    )
    fig.add_trace(go.Scatter(
        x=cost_per_cycle["cycle"], y=cost_per_cycle["totalCost"],
        mode="lines+markers", name="total cost",
        line=dict(width=2), showlegend=False,
    ), row=2, col=1)

    fig.update_layout(
        title=title, xaxis2_title="Cycle",
        template="plotly_dark", paper_bgcolor="#1e1e1e", plot_bgcolor="#1e1e1e",
        legend=dict(orientation="h", yanchor="bottom", y=1.02),
    )
    fig.update_yaxes(title_text="Replicas", dtick=1, row=1, col=1)
    fig.update_yaxes(title_text="Cost", row=2, col=1)
    return fig


def fig_capacity(df):
    title = "Capacity: Accelerators Allocated vs Available"
    if df.empty or "type" not in df.columns:
        return _empty(title)

    palette = [
        "#636efa", "#ef553b", "#00cc96", "#ab63fa",
        "#ffa15a", "#19d3f3", "#ff6692", "#b6e880",
    ]
    acc_types = sorted(df["type"].unique())
    colors = {t: palette[i % len(palette)] for i, t in enumerate(acc_types)}

    fig = go.Figure()
    for acc_type in acc_types:
        s = df[df["type"] == acc_type].sort_values("cycle")
        color = colors[acc_type]

        fig.add_trace(go.Scatter(
            x=s["cycle"], y=s["allocated"],
            mode="lines+markers", name=f"{acc_type} allocated",
            line=dict(color=color), legendgroup=acc_type,
        ))
        fig.add_trace(go.Scatter(
            x=s["cycle"], y=s["available"],
            mode="lines", name=f"{acc_type} available",
            line=dict(color=color, dash="dash"),
            legendgroup=acc_type, showlegend=True,
        ))

    fig.update_layout(
        title=title, xaxis_title="Cycle",
        template="plotly_dark", paper_bgcolor="#1e1e1e", plot_bgcolor="#1e1e1e",
        legend=dict(orientation="h", yanchor="bottom", y=1.02),
        yaxis=dict(title="Accelerator Units", dtick=1, rangemode="tozero"),
    )
    return fig


def fig_internals(df, servers_df=None):
    title = "EKF Parameters: alpha / beta / gamma"
    if df.empty:
        return _empty(title)

    # Filter to only (model, accelerator) pairs actually assigned to deployed servers
    if servers_df is not None and not servers_df.empty:
        active_pairs = set(
            zip(servers_df["model"].tolist(), servers_df["accelerator"].tolist())
        )
        df = df[df.apply(lambda r: (r["model"], r["acc"]) in active_pairs, axis=1)]

    if df.empty:
        return _empty(title)

    fig = make_subplots(
        rows=3, cols=1, shared_xaxes=True,
        subplot_titles=("alpha (base overhead)", "beta (compute scaling)", "gamma (memory scaling)"),
        vertical_spacing=0.1,
    )

    for combo in df[["model", "acc"]].drop_duplicates().itertuples(index=False):
        mask = (df["model"] == combo.model) & (df["acc"] == combo.acc)
        s = df[mask].sort_values("cycle")
        label = f"{combo.model}/{combo.acc}"

        fig.add_trace(go.Scatter(
            x=s["cycle"], y=s["alpha"],
            mode="lines+markers", name=label, legendgroup=label,
        ), row=1, col=1)
        fig.add_trace(go.Scatter(
            x=s["cycle"], y=s["beta"],
            mode="lines+markers", name=label, legendgroup=label, showlegend=False,
        ), row=2, col=1)
        fig.add_trace(go.Scatter(
            x=s["cycle"], y=s["gamma"],
            mode="lines+markers", name=label, legendgroup=label, showlegend=False,
        ), row=3, col=1)

    fig.update_layout(
        title=title, xaxis3_title="Cycle",
        template="plotly_dark", paper_bgcolor="#1e1e1e", plot_bgcolor="#1e1e1e",
        legend=dict(orientation="h", yanchor="bottom", y=1.02),
    )
    return fig


# ---------------------------------------------------------------------------
# App layout and callbacks
# ---------------------------------------------------------------------------

app = Dash(__name__)
app.title = "Inferno Control Loop"

app.layout = html.Div(
    style={"backgroundColor": "#1e1e1e", "fontFamily": "monospace", "padding": "12px"},
    children=[
        html.H2("Inferno Control Loop", style={"color": "#e0e0e0", "marginBottom": "4px"}),
        html.Div(
            f"Log: {LOG_PATH}  |  Refresh: {REFRESH_MS}ms",
            style={"color": "#888", "fontSize": "12px", "marginBottom": "16px"},
        ),
        dcc.Interval(id="tick", interval=REFRESH_MS, n_intervals=0),
        dcc.Graph(id="workload-panel", style={"marginBottom": "8px"}),
        dcc.Graph(id="performance-panel", style={"marginBottom": "8px"}),
        dcc.Graph(id="controls-panel", style={"marginBottom": "8px"}),
        dcc.Graph(id="capacity-panel", style={"marginBottom": "8px"}),
        dcc.Graph(id="internals-panel"),
    ],
)


@app.callback(
    [
        Output("workload-panel", "figure"),
        Output("performance-panel", "figure"),
        Output("controls-panel", "figure"),
        Output("capacity-panel", "figure"),
        Output("internals-panel", "figure"),
    ],
    [Input("tick", "n_intervals")],
)
def update(_n):
    servers_df, internals_df, capacity_df = load_data()
    return (
        fig_workload(servers_df),
        fig_performance(servers_df),
        fig_controls(servers_df),
        fig_capacity(capacity_df),
        fig_internals(internals_df, servers_df),
    )


if __name__ == "__main__":
    print(f"Dashboard running at http://localhost:{PORT}")
    print(f"Reading log from: {LOG_PATH}")
    app.run(debug=False, port=PORT)
