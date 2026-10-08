import { Link } from "react-router";
import { useDocumentTitle } from "../lib/useDocumentTitle";

export function NotFound() {
  useDocumentTitle("Page not found");
  return (
    <>
      <h1 className="text-3xl font-bold tracking-tight">404</h1>
      <p className="mt-2 text-sm text-gray-600">
        No route matched.{" "}
        <Link to="/" className="text-indigo-600 hover:underline">
          Go home
        </Link>
        .
      </p>
    </>
  );
}
