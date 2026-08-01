/** @type {import('orval').ConfigExternal} */
module.exports = {
	factory: {
		input: {
			target: "../internal/api/openapi.yaml",
		},
		output: {
			target: "./src/api/generated.ts",
			client: "react-query",
			mode: "single",
			clean: true,
		},
	},
};
