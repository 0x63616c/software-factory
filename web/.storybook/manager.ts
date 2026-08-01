import { addons } from "storybook/manager-api";
import { create } from "storybook/theming/create";

const factoryTheme = create({
  base: "dark",
  brandTitle: "Software Factory",

  appBg: "#0b0b0c",
  appContentBg: "#141416",
  appPreviewBg: "#0b0b0c",
  appBorderColor: "rgba(255,255,255,0.08)",

  textColor: "#e8e8ea",
  textMutedColor: "#9a9aa0",

  barTextColor: "#9a9aa0",
  barHoverColor: "#e8e8ea",
  barBg: "#141416",

  inputBg: "#1c1c1f",
  inputBorder: "rgba(255,255,255,0.08)",
  inputTextColor: "#e8e8ea",
});

addons.setConfig({ theme: factoryTheme });
