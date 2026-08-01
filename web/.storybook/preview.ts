import type { Preview } from "@storybook/react-vite";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { createElement } from "react";
import { create } from "storybook/theming/create";
import "../src/styles/app.css";

// Matches manager.ts so the Docs page (MDX/autodocs) is never white.
const factoryTheme = create({
  base: "dark",
  appBg: "#0b0b0c",
  appContentBg: "#141416",
  appPreviewBg: "#0b0b0c",
  appBorderColor: "rgba(255,255,255,0.08)",
  textColor: "#e8e8ea",
  textMutedColor: "#9a9aa0",
});

// Every story renders inside a QueryClientProvider: any component that calls
// a generated react-query hook (e.g. TranscriptViewer, collapsed or not)
// needs one in its tree just to construct, the same way main.tsx wraps the
// real App. retry is off so an inevitable-in-Storybook failed fetch (there is
// no API behind Storybook) resolves to an error state immediately instead of
// retrying for real time.
const preview: Preview = {
  parameters: {
    docs: { theme: factoryTheme },
    backgrounds: {
      default: "factory",
      values: [{ name: "factory", value: "#0b0b0c" }],
    },
  },
  decorators: [
    (Story) => {
      const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
      return createElement(QueryClientProvider, { client: queryClient }, createElement(Story));
    },
  ],
};

export default preview;
