import { QueryClientProvider } from "@tanstack/react-query";
import axios from "axios";
import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import { App } from "@/App";
import { createQueryClient } from "@/queryClient";
import "@/styles/app.css";

// Same-origin proxy (ADR-0012 "Exposure and authentication" / nginx.conf):
// the generated client's requests (e.g. `/v1/build`) go through `/api/*`,
// which nginx forwards to the API's in-cluster Service. Set once, here,
// rather than threading a base URL through every generated call site.
axios.defaults.baseURL = "/api";

const root = document.getElementById("root");
if (!root) throw new Error("software-factory console: #root missing from index.html");

const queryClient = createQueryClient();

createRoot(root).render(
  <StrictMode>
    <QueryClientProvider client={queryClient}>
      <App />
    </QueryClientProvider>
  </StrictMode>,
);
