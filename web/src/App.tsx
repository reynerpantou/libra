import { Navigate, Route, Routes } from "react-router-dom";
import Layout from "./components/Layout";
import { Loading } from "./components/ui";
import { useAuth } from "./lib/auth";
import Attributes from "./pages/Attributes";
import BusinessDetail from "./pages/BusinessDetail";
import Businesses from "./pages/Businesses";
import ExperimentDetail from "./pages/ExperimentDetail";
import ExperimentForm from "./pages/ExperimentForm";
import Experiments from "./pages/Experiments";
import Integrate from "./pages/Integrate";
import Layers from "./pages/Layers";
import Diversions from "./pages/Diversions";
import Tuning from "./pages/Tuning";
import TuningDetail from "./pages/TuningDetail";
import TuningForm from "./pages/TuningForm";
import Login, { SignInLink } from "./pages/Login";
import Setup from "./pages/Setup";
import Pipeline from "./pages/Pipeline";
import PlatformDetail from "./pages/PlatformDetail";
import Parameters from "./pages/Parameters";
import Settings from "./pages/Settings";
import Tools from "./pages/Tools";

function Protected({ children }: { children: JSX.Element }) {
  const { user, loading } = useAuth();
  if (loading) return <Loading />;
  if (!user) return <Navigate to="/login" replace />;
  return children;
}

export default function App() {
  return (
    <Routes>
      <Route path="/login" element={<Login />} />
      <Route path="/login/link" element={<SignInLink />} />
      <Route path="/setup" element={<Setup />} />
      <Route
        element={
          <Protected>
            <Layout />
          </Protected>
        }
      >
        <Route index element={<Navigate to="/experiments" replace />} />
        <Route path="/experiments" element={<Experiments />} />
        <Route path="/experiments/new" element={<ExperimentForm />} />
        <Route path="/experiments/:id" element={<ExperimentDetail />} />
        <Route path="/experiments/:id/edit" element={<ExperimentForm />} />
        <Route path="/businesses" element={<Businesses />} />
        <Route path="/businesses/:id" element={<BusinessDetail />} />
        <Route path="/platforms/:pid" element={<PlatformDetail />} />
        <Route path="/parameters" element={<Parameters />} />
        <Route path="/layers" element={<Layers />} />
        <Route path="/diversions" element={<Diversions />} />
        <Route path="/tuning" element={<Tuning />} />
        <Route path="/tuning/new" element={<TuningForm />} />
        <Route path="/tuning/:id" element={<TuningDetail />} />
        <Route path="/tuning/:id/edit" element={<TuningForm />} />
        <Route path="/attributes" element={<Attributes />} />
        <Route path="/tools" element={<Tools />} />
        <Route path="/pipeline" element={<Pipeline />} />
        <Route path="/integrate" element={<Integrate />} />
        <Route path="/settings" element={<Settings />} />
        <Route path="*" element={<Navigate to="/experiments" replace />} />
      </Route>
    </Routes>
  );
}
